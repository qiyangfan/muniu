// 流式转发：逐事件转发，空行切完整事件并缓冲半帧；响应头固定三项；三层超时、断连传导、断言出口。
package proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/solvela/muniu/internal/adapter"
	"github.com/solvela/muniu/internal/failover"
	"github.com/solvela/muniu/internal/logging"
	"github.com/solvela/muniu/internal/protocol"
)

// sseBufSize 读写缓冲，性能预算为每请求稳态不超过 2×32KB。
const sseBufSize = 32 << 10

// eventResult 读帧 goroutine 投递的单个完整事件。
type eventResult struct {
	raw     []byte // 事件原始字节，逐字节透传物
	payload string // data 行载荷，[DONE] 或 JSON
	err     error
}

// attemptStream 单候选流式执行。首个内容帧前对客户端零输出，首 token 前失败可安全换道重放；
// 首个内容帧即开闸：写响应头并缓冲事件，此后失败走断言出口，即流内错误帧加 [DONE]。
// 计量包括 usage 帧抽取、delta 文本累计作估算 fallback、首 token 延迟。
func (p *Proxy) attemptStream(w http.ResponseWriter, r *http.Request, ad adapter.Adapter, out *adapter.Outbound, m *meterState) (bool, failover.ErrorClass) {
	m.stream, m.normalize = true, ad.NormalizeUsage
	opCtx, opCancel := context.WithCancel(r.Context())
	defer opCancel()
	// 触发标志用原子位：Timer.Stop 不幂等，二次调用恒 false，不能作为是否超时的判据
	var firstTokFired atomic.Bool
	firstTok := time.AfterFunc(p.timeouts.FirstToken, func() {
		firstTokFired.Store(true)
		opCancel()
	})
	defer firstTok.Stop()

	upReq, err := p.outboundRequest(r, opCtx, out)
	if err != nil {
		p.writeError(w, r, protocol.TypeInternal, "internal", "出站请求构造失败")
		return true, failover.ClassUnknown
	}
	resp, err := p.client.Do(upReq)
	if err != nil {
		return p.classifyTransportErr(w, r, err, &firstTokFired, m)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		// 上游拒绝即流未开始：就地读错误体归类，交候选循环统一出口；
		// 不重发第二次请求，429 场景会双重计数上游限流器，计量 stream 列保持客户端意图
		b, _ := io.ReadAll(io.LimitReader(resp.Body, maxUpstreamErrBody))
		class := ad.Classify(resp.StatusCode, b)
		m.status, m.class = "error", class
		if class == failover.ClassRateLimited {
			m.retryAfter = parseRetryAfter(resp.Header.Get("Retry-After"))
		}
		m.upstreamMsg = upstreamMessage(b, "上游供应商返回错误")
		p.logger.Warn("上游非 2xx",
			"request_id", loggingRequestID(r.Context()),
			"status", resp.StatusCode,
			"error_detail", truncateForLog(b),
		)
		return true, class
	}

	// 读帧 goroutine：阻塞读交给它，主 select 才能同时盯超时与断连
	br := bufio.NewReaderSize(resp.Body, sseBufSize)
	evCh := make(chan eventResult, 8)
	go func() {
		// close 与投递对 opCtx 取消竞态的兜底：即使最后一个错误被 Done 分支吞掉，
		// close 也会唤醒 handler，由其按取消原因归类，绝不悬挂
		defer close(evCh)
		for {
			raw, payload, err := readEvent(br)
			select {
			case evCh <- eventResult{raw: raw, payload: payload, err: err}:
			case <-opCtx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()

	// 每流测序优先，实现 adapter.StreamSequencer 者启用；gemini 等无序号 wire 需要跨帧工具调用计数，
	// 未实现者维持无状态 StreamEvent 既有行为。
	streamEvent := ad.StreamEvent
	if sq, ok := ad.(adapter.StreamSequencer); ok {
		streamEvent = sq.NewStreamSequencer()
	}

	var (
		pending  []byte // 开闸前缓冲的完整事件，半帧绝不进入
		gateOpen bool   // 是否已过首 token，已向客户端开闸输出
		idleC    <-chan time.Time
		idleT    *time.Timer
	)
	for {
		select {
		case <-r.Context().Done():
			// 客户端断连：cancel 沿 context 传导至上游，静默退出，不换道不计失败；
			// 已过首 token 的部分内容按估算计量，判定表口径为计量已发生部分
			opCancel()
			m.status = "client_cancel"
			if gateOpen {
				m.est = true
			}
			return true, failover.ClassContextCanceled

		case <-idleC:
			// 帧间超时即上游僵死，已过首 token 走断言出口，已转发部分按估算
			opCancel()
			m.status, m.class = "error", failover.ClassMidStreamFail
			m.est = true
			p.writeStreamAbort(w, r, "mid_stream_fail")
			return true, failover.ClassMidStreamFail

		case res, ok := <-evCh:
			if !ok {
				// 读者退出且最终状态被取消竞态吞掉：按取消原因归类
				opCancel()
				if r.Context().Err() != nil {
					m.status = "client_cancel"
					if gateOpen {
						m.est = true
					}
					return true, failover.ClassContextCanceled
				}
				if firstTokFired.Load() {
					m.status, m.class = "error", failover.ClassTimeoutFirst
					return false, failover.ClassTimeoutFirst
				}
				m.status, m.class = "error", failover.ClassMidStreamFail
				return true, failover.ClassMidStreamFail
			}
			if res.err != nil {
				opCancel()
				if r.Context().Err() != nil {
					// 客户端断连的常见时序之一：cancel 使在途读报错，错误事件先于 Done 分支抵达
					m.status = "client_cancel"
					if gateOpen {
						m.est = true
					}
					return true, failover.ClassContextCanceled
				}
				// 无终止标记协议如 gemini 实现 StreamCloseDone：干净关流即正常收尾，等价 IsDone；
				// 未实现者维持断流按 mid_stream_fail 处理的既有契约。
				if errors.Is(res.err, io.EOF) && gateOpen {
					if scd, ok := ad.(adapter.StreamCloseDone); ok && scd.StreamCloseIsDone() {
						m.markFirstToken()
						if len(m.usageFrames) == 0 {
							m.est = true
						}
						if fz, ok := w.(streamFinalizer); ok {
							fz.finishClean() // 翻译 wrapper 合成协议终态，如 Gemini 终帧或 message_delta+stop
						} else {
							// 裸 chat writer 补 [DONE] 干净收尾，上游无终止标记时契约由网关兑现
							_, _ = w.Write([]byte("data: [DONE]\n\n"))
							flush(w)
						}
						return true, ""
					}
				}
				if !gateOpen {
					if firstTokFired.Load() {
						m.status, m.class = "error", failover.ClassTimeoutFirst
						return false, failover.ClassTimeoutFirst // 首 token 超时，零输出，可换道
					}
					m.status, m.class = "error", failover.ClassMidStreamFail
					return false, failover.ClassMidStreamFail // 上游断流未过首 token，可换道
				}
				// 已过首 token 走断言出口：客户端已收到部分内容，如实断言并计量已发生部分
				m.status, m.class = "error", failover.ClassMidStreamFail
				m.est = true
				p.writeStreamAbort(w, r, "mid_stream_fail")
				return true, failover.ClassMidStreamFail
			}

			passthrough, meta, perr := streamEvent([]byte(res.payload))
			if perr != nil {
				opCancel()
				p.logger.Warn("上游 SSE 帧解析失败", "request_id", loggingRequestID(r.Context()), "error_detail", perr.Error())
				if !gateOpen {
					m.status, m.class = "error", failover.ClassMidStreamFail
					return false, failover.ClassMidStreamFail
				}
				m.status, m.class = "error", failover.ClassMidStreamFail
				p.writeStreamAbort(w, r, "mid_stream_fail")
				return true, failover.ClassMidStreamFail
			}
			// 计量消费统一走适配器翻译帧，基座透传时 passthrough 即原字节；
			// 桥接原始事件是 Anthropic 形态，按 OpenAI chunk 解析恒为空，估算会静默归零
			src := passthrough
			if len(src) == 0 {
				src = []byte(res.payload)
			}
			// 内容与推理入估算累计，tool_call 参数一并计入，finish_reason 捕获供语义缓存准入
			m.accumulateDelta(src)

			if meta.IsUsage {
				// usage 帧：抽内层 usage 对象供归一，多帧合并适配桥接 start/delta 分离
				var frame struct {
					Usage json.RawMessage `json:"usage"`
				}
				if err := json.Unmarshal(src, &frame); err == nil && len(frame.Usage) > 0 && string(frame.Usage) != "null" {
					m.usageFrames = append(m.usageFrames, frame.Usage)
					// 预算真值并入，真值覆盖估算，归一失败不影响计量主路径
					if m.budget > 0 && m.normalize != nil {
						if u, uerr := m.normalize(frame.Usage); uerr == nil {
							m.budgetObserveUsage(u)
						}
					}
				}
			}
			// 请求级成本预算流中闸：终态帧不拦，响应已完成后事后无中断意义，其余帧超限即断言；
			// 已开闸走流内错误帧干净收尾并保留已收部分，未开闸回 402 信封
			if !meta.IsDone && m.budgetExceeded() {
				opCancel()
				m.status, m.class = "error", failover.ClassCostBudget
				if len(m.usageFrames) == 0 {
					m.est = true
				}
				if gateOpen {
					p.writeStreamAbortCode(w, r, protocol.TypeQuotaExceeded, costBudgetExceededCode,
						"单请求成本预算已超限,已生成部分保留")
				} else {
					p.writeError(w, r, protocol.TypeQuotaExceeded, costBudgetExceededCode, "单请求成本预算已超限")
				}
				return true, failover.ClassCostBudget
			}

			if meta.IsDone && !gateOpen {
				// 未开闸即收尾且上游无内容帧：放行缓冲事件干净收尾，无内容帧不计首 token 延迟
				p.openGate(w)
				p.writeEvents(w, pending)
				p.writeEvent(w, res.raw)
				if len(m.usageFrames) == 0 {
					m.est = true
				}
				return true, ""
			}
			if !gateOpen {
				pending = append(pending, wireOf(ad, passthrough, res.payload, res.raw)...)
				if meta.HasContent {
					// 首 token：停 first_token，启 chunk_idle，开闸放行缓冲
					gateOpen = true
					m.markFirstToken()
					firstTok.Stop()
					p.openGate(w)
					p.writeEvents(w, pending)
					pending = nil
					idleT = time.NewTimer(p.timeouts.ChunkIdle)
					defer idleT.Stop()
					idleC = idleT.C
				}
				continue
			}
			p.writeEvent(w, wireOf(ad, passthrough, res.payload, res.raw))
			idleT.Reset(p.timeouts.ChunkIdle) // 帧间计时重置
			if meta.IsDone {
				if len(m.usageFrames) == 0 {
					m.est = true // usage 缺失走估算路径，累计 delta 已缓冲
				}
				return true, ""
			}
		}
	}
}

// wireOf 计算事件应写出的线上字节：适配器判定不可见即空翻译优先跳过；
// 基座透传返回原字节，桥接翻译返回 data: 加翻译 chunk 再加空行的封装。
func wireOf(ad adapter.Adapter, passthrough []byte, payload string, raw []byte) []byte {
	// 不可见事件即适配器返回 nil 翻译，如心跳与桥接 ping，必须最先判定：
	// payload 为空串时 bytes.Equal(nil, 空) 恒真，若先做相等判定会把原始事件字节漏给客户端
	if len(passthrough) == 0 {
		return nil
	}
	if bytes.Equal(passthrough, []byte(payload)) {
		return raw // 透传适配器原字节
	}
	out := make([]byte, 0, len(passthrough)+10)
	out = append(out, "data: "...)
	out = append(out, passthrough...)
	out = append(out, "\n\n"...)
	return out
}

// openGate 开闸：写固定三项响应头并冲刷。
func (p *Proxy) openGate(w http.ResponseWriter) {
	h := w.Header()
	if h.Get("Content-Type") == "" {
		h.Set("Content-Type", "text/event-stream")
	}
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flush(w)
}

func (p *Proxy) writeEvents(w http.ResponseWriter, events []byte) {
	if len(events) == 0 {
		return
	}
	_, _ = w.Write(events)
	flush(w)
}

// writeEvent 写单个事件并冲刷，逐事件转发禁止攒整包。
func (p *Proxy) writeEvent(w http.ResponseWriter, raw []byte) {
	_, _ = w.Write(raw)
	flush(w)
}

// writeStreamAbort 断言出口：流内错误帧加补发 [DONE]，让客户端干净收尾。
func (p *Proxy) writeStreamAbort(w http.ResponseWriter, r *http.Request, code string) {
	p.writeStreamAbortCode(w, r, protocol.TypeUpstreamError, code, "上游流中断,已生成部分可能不完整")
}

// writeStreamAbortCode 断言出口通用形态，错误类型、码、文案参数化，预算中断与上游断流共用。
func (p *Proxy) writeStreamAbortCode(w http.ResponseWriter, r *http.Request, typ, code, msg string) {
	b, err := marshalEnvelope(protocol.NewErrorEnvelope(
		typ, code, msg, logging.RequestIDFrom(r.Context())))
	if err != nil {
		return
	}
	_, _ = w.Write([]byte("data: " + string(b) + "\n\ndata: [DONE]\n\n"))
	flush(w)
}

// streamFinalizer 无终止标记协议的干净收尾钩子，由协议 wrapper 实现。
// 上游干净关流即无 [DONE] 时由 attemptStream 调用，wrapper 合成协议终态，
// 如 Gemini 终帧或 Anthropic message_delta+message_stop；裸 chat writer 无此钩子。
type streamFinalizer interface{ finishClean() }

// flush 尽力冲刷，httptest 与 net/http 的 ResponseWriter 均实现 Flusher。
func flush(w http.ResponseWriter) {
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// readEvent 从 br 读取一个完整 SSE 事件，以空行分隔。
// 逐字节保留原始行，多行 data 合法，禁止按行重组透传物；事件终止空行属于事件字节，必须一并透传。
// EOF 时若有未收尾的半帧返回 ErrUnexpectedEOF，半帧残留绝不解析成事件。
func readEvent(br *bufio.Reader) ([]byte, string, error) {
	var raw strings.Builder
	var data []string
	sawLine := false
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				if sawLine {
					return nil, "", io.ErrUnexpectedEOF // 半帧
				}
				return nil, "", io.EOF // 干净收尾，上游在事件边界结束
			}
			return nil, "", err
		}
		trimmed := strings.TrimRight(line, "\r\n")
		if trimmed == "" {
			if !sawLine {
				continue // 事件间或流首部的连续空行，不属于任何事件
			}
			raw.WriteString(line) // 终止空行是事件字节的一部分，必须透传
			return []byte(raw.String()), strings.Join(data, "\n"), nil
		}
		sawLine = true
		raw.WriteString(line)
		if strings.HasPrefix(trimmed, "data:") {
			data = append(data, strings.TrimSpace(strings.TrimPrefix(trimmed, "data:")))
		}
	}
}
