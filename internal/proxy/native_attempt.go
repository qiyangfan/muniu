// 直通路径单候选执行：出站用原始体加鉴权注入；响应恒等透传，writer native 模式位；
// 计量与缓存拼包经 meta-only 读侧与翻译路径同口径；错误信封按路径分域：
// 不换道类原样透传，流内失败诚实终止不合成帧，直通不伪造协议形态。
package proxy

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/solvela/muniu/internal/adapter"
	"github.com/solvela/muniu/internal/failover"
	"github.com/solvela/muniu/internal/protocol"
)

// attemptNativeNonStream 直通非流式：2xx 整包原样回写、原生 usage 归一、canonical 侧双存衍生；
// 非 2xx 归类并暂存原样错误体，ActionReturn 时透传，换道类交候选循环。
func (p *Proxy) attemptNativeNonStream(w http.ResponseWriter, r *http.Request, upstream *http.Client, ad adapter.Adapter,
	np adapter.NativePreparer, out *adapter.Outbound, m *meterState, nat *inboundNative, cand failover.Candidate) (bool, failover.ErrorClass) {
	m.stream, m.normalize, m.nativePath = false, np.NormalizeNativeUsage, true
	defer func() { p.record(r, nat.model, cand.Model, cand, string(nat.raw), m) }()
	opCtx, opCancel := context.WithCancel(r.Context())
	defer opCancel()
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
	resp, err := upstream.Do(upReq)
	if err != nil {
		return p.classifyTransportErr(w, r, err, &firstTokFired, m)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, maxUpstreamErrBody))
		class := ad.Classify(resp.StatusCode, b)
		m.creditEvidence = isCreditExhausted(resp.StatusCode, b)
		m.status, m.class = "error", class
		if class == failover.ClassRateLimited {
			m.retryAfter = parseRetryAfter(resp.Header.Get("Retry-After"))
		}
		m.upstreamBody, m.upstreamStatus = b, resp.StatusCode
		m.upstreamCT = resp.Header.Get("Content-Type")
		m.upstreamMsg = upstreamMessage(b, "上游供应商返回错误")
		p.logger.Warn("上游非 2xx",
			"request_id", loggingRequestID(r.Context()),
			"status", resp.StatusCode,
			"error_detail", truncateForLog(b),
		)
		return true, class
	}

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxNonStreamBody))
	if err != nil {
		// 上游响应中断：未向客户端写过任何字节，交候选循环，与翻译路径同判定
		m.status, m.class = "error", failover.ClassMidStreamFail
		p.logger.Warn("直通非流式响应读取失败", "request_id", loggingRequestID(r.Context()), "error_detail", err.Error())
		return true, failover.ClassMidStreamFail
	}
	// usage 原生形态归一并捕获 responses 响应 id，供亲和回写
	var probe struct {
		ID    string          `json:"id"`
		Usage json.RawMessage `json:"usage"`
	}
	_ = json.Unmarshal(respBody, &probe)
	if len(probe.Usage) > 0 && string(probe.Usage) != "null" {
		m.usageFrames = append(m.usageFrames, probe.Usage)
	} else {
		m.est = true
	}
	if nat.proto == protocol.InboundResponses && probe.ID != "" {
		m.nativeRespID = probe.ID
	}
	// canonical 侧衍生双存，失败仅 canonical 降级，请求正常返回
	m.outBody = respBody
	m.canonicalBody = canonicalFromNative(ad, nat.proto, respBody)

	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(respBody)
	return true, ""
}

// attemptNativeStream 直通流式：逐事件原字节透传，meta-only 读侧供计量与首 token 判定；
// 流内失败帧诚实透传后终止，不合成任何非上游形态的帧，错误信封按路径分域。
func (p *Proxy) attemptNativeStream(w http.ResponseWriter, r *http.Request, upstream *http.Client, ad adapter.Adapter,
	np adapter.NativePreparer, out *adapter.Outbound, m *meterState, nat *inboundNative, cand failover.Candidate) (bool, failover.ErrorClass) {
	m.stream, m.normalize, m.nativePath = true, np.NormalizeNativeUsage, true
	defer func() { p.record(r, nat.model, cand.Model, cand, string(nat.raw), m) }()
	opCtx, opCancel := context.WithCancel(r.Context())
	defer opCancel()
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
	resp, err := upstream.Do(upReq)
	if err != nil {
		return p.classifyTransportErr(w, r, err, &firstTokFired, m)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		// 流未开始：读错误体归类，换道类交循环，不换道类由循环透传原样信封
		b, _ := io.ReadAll(io.LimitReader(resp.Body, maxUpstreamErrBody))
		class := ad.Classify(resp.StatusCode, b)
		m.creditEvidence = isCreditExhausted(resp.StatusCode, b)
		m.status, m.class = "error", class
		if class == failover.ClassRateLimited {
			m.retryAfter = parseRetryAfter(resp.Header.Get("Retry-After"))
		}
		m.upstreamBody, m.upstreamStatus = b, resp.StatusCode
		m.upstreamCT = resp.Header.Get("Content-Type")
		m.upstreamMsg = upstreamMessage(b, "上游供应商返回错误")
		p.logger.Warn("上游非 2xx",
			"request_id", loggingRequestID(r.Context()),
			"status", resp.StatusCode,
			"error_detail", truncateForLog(b),
		)
		return true, class
	}

	br := bufio.NewReaderSize(resp.Body, sseBufSize)
	evCh := make(chan eventResult, 8)
	go func() {
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

	var (
		pending  []byte // 开闸前缓冲的完整事件，原字节
		gateOpen bool
		idleC    <-chan time.Time
		idleT    *time.Timer
		wire     []byte // 成功流的透传字节累计，缓存双存的原生侧，失败路径不消费
	)
	for {
		select {
		case <-r.Context().Done():
			opCancel()
			m.status = "client_cancel"
			if gateOpen {
				m.est = true
			}
			return true, failover.ClassContextCanceled

		case <-idleC:
			// 帧间超时即上游僵死：直通域内已透传字节如实终止，不合成非上游形态的帧
			opCancel()
			m.status, m.class = "error", failover.ClassMidStreamFail
			m.est = true
			return true, failover.ClassMidStreamFail

		case res, ok := <-evCh:
			if !ok {
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
					m.status = "client_cancel"
					if gateOpen {
						m.est = true
					}
					return true, failover.ClassContextCanceled
				}
				if !gateOpen {
					if firstTokFired.Load() {
						m.status, m.class = "error", failover.ClassTimeoutFirst
						return false, failover.ClassTimeoutFirst
					}
					m.status, m.class = "error", failover.ClassMidStreamFail
					return false, failover.ClassMidStreamFail // 上游断流未过首 token，可换道
				}
				// 已过首 token：出口为如实终止已透传字节，不合成错误帧
				m.status, m.class = "error", failover.ClassMidStreamFail
				m.est = true
				return true, failover.ClassMidStreamFail
			}

			internal, meta, ferr := np.NativeStreamEvent([]byte(res.payload))
			if ferr != nil {
				// 原生流内失败帧：诚实透传该事件后终止，由上游裁决 wire，直通不伪造协议形态、不换道
				opCancel()
				p.logger.Warn("直通流内失败帧", "request_id", loggingRequestID(r.Context()), "error_detail", ferr.Error())
				if !gateOpen {
					p.openGate(w)
					p.writeEvents(w, pending)
				}
				p.writeEvent(w, res.raw)
				m.status, m.class = "error", failover.ClassMidStreamFail
				m.est = true
				return true, failover.ClassMidStreamFail
			}
			// 计量与首 token 判定：内部 chunk 与翻译路径同口径消费，客户端恒收原字节
			src := internal
			if len(src) == 0 {
				src = []byte(res.payload)
			}
			m.accumulateDelta(src)
			if meta.IsUsage {
				var frame struct {
					Usage json.RawMessage `json:"usage"`
				}
				if err := json.Unmarshal(src, &frame); err == nil && len(frame.Usage) > 0 && string(frame.Usage) != "null" {
					m.usageFrames = append(m.usageFrames, frame.Usage)
					// 预算真值并入：直通路径无 delta 估算形态，判定落在 usage 帧
					if m.budget > 0 && m.normalize != nil {
						if u, uerr := m.normalize(frame.Usage); uerr == nil {
							m.budgetObserveUsage(u)
						}
					}
				}
			}
			// 直通流预算闸边界：仅未开闸可拦，402 信封保证客户端零字节污染；
			// 已开闸后无原生协议错误帧合成机制，不中断，流内成本控制由翻译路径全量覆盖
			if !gateOpen && !meta.IsDone && m.budgetExceeded() {
				opCancel()
				m.status, m.class = "error", failover.ClassCostBudget
				if len(m.usageFrames) == 0 {
					m.est = true
				}
				p.writeError(w, r, protocol.TypeQuotaExceeded, costBudgetExceededCode, "单请求成本预算已超限")
				return true, failover.ClassCostBudget
			}
			if nat.proto == protocol.InboundResponses {
				if id := responsesEventID([]byte(res.payload)); id != "" {
					m.nativeRespID = id
				}
			}
			wire = append(wire, res.raw...)

			if meta.IsDone && !gateOpen {
				// 未开闸即收尾，无内容帧：放行缓冲原字节干净收尾
				p.openGate(w)
				p.writeEvents(w, pending)
				p.writeEvent(w, res.raw)
				if len(m.usageFrames) == 0 {
					m.est = true
				}
				m.outBody, m.canonicalBody = wire, m.assembleStreamBody(cand.Model)
				return true, ""
			}
			if !gateOpen {
				pending = append(pending, res.raw...)
				if meta.HasContent {
					// 首 token：停 first_token，启 chunk_idle，开闸放行缓冲原字节
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
			p.writeEvent(w, res.raw)
			idleT.Reset(p.timeouts.ChunkIdle)
			if meta.IsDone {
				if len(m.usageFrames) == 0 {
					m.est = true
				}
				m.outBody, m.canonicalBody = wire, m.assembleStreamBody(cand.Model)
				return true, ""
			}
		}
	}
}

// writeUpstreamPassthrough 直通不换道类错误：上游信封原样到达客户端，状态、头、体零改写。
func (p *Proxy) writeUpstreamPassthrough(w http.ResponseWriter, m *meterState) {
	if m.upstreamCT != "" {
		w.Header().Set("Content-Type", m.upstreamCT)
	} else {
		w.Header().Set("Content-Type", "application/json")
	}
	status := m.upstreamStatus
	if status == 0 {
		status = http.StatusBadGateway
	}
	w.WriteHeader(status)
	_, _ = w.Write(m.upstreamBody)
}

// canonicalFromNative 直通响应转 canonical chat 形态，作为缓存双存的 canonical 侧：
// messages 走桥接翻译 RewriteResponse，responses 走受限逆映射；失败返回 nil，
// 仅降级为跨协议不可命中，不失败请求、不损同构回放。
func canonicalFromNative(ad adapter.Adapter, proto protocol.InboundProtocol, body []byte) []byte {
	switch proto {
	case protocol.InboundMessages:
		canonical, _, err := ad.RewriteResponse(body)
		if err != nil {
			return nil
		}
		return canonical
	case protocol.InboundResponses:
		out, err := protocol.ChatFromResponsesObject(body)
		if err != nil {
			return nil
		}
		return out
	default:
		return nil
	}
}

// responsesEventID 轻量提取 responses 事件的 response.id，粘性亲和回写载体。
func responsesEventID(payload []byte) string {
	var probe struct {
		Type     string `json:"type"`
		Response struct {
			ID string `json:"id"`
		} `json:"response"`
	}
	if json.Unmarshal(payload, &probe) != nil {
		return ""
	}
	switch probe.Type {
	case "response.created", "response.in_progress", "response.completed", "response.incomplete":
		return probe.Response.ID
	default:
		return ""
	}
}
