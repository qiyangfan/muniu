// 非流式转发：出站调用、状态透传，2xx 整包回写，非 2xx 归一为错误信封，走状态映射表。
// 三层超时中的 dial(Transport)与 first_token 经 AfterFunc 取消在此生效；非流式没有已过首 token 态。
package proxy

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/solvela/muniu/internal/adapter"
	"github.com/solvela/muniu/internal/failover"
	"github.com/solvela/muniu/internal/protocol"
)

// maxUpstreamErrBody 上游错误 body 读取上限 1MB，仅用于归一化与日志。
const maxUpstreamErrBody = 1 << 20

// maxNonStreamBody 非流式响应缓冲上限 64MB，usage 抽取需全量缓冲。
const maxNonStreamBody = 64 << 20

// attemptNonStream 单候选非流式执行。返回 false 表示失败且未向客户端输出任何字节，可换道。
func (p *Proxy) attemptNonStream(w http.ResponseWriter, r *http.Request, upstream *http.Client, ad adapter.Adapter, out *adapter.Outbound, m *meterState) (bool, failover.ErrorClass) {
	m.stream, m.normalize = false, ad.NormalizeUsage
	opCtx, opCancel := context.WithCancel(r.Context())
	defer opCancel()
	// 触发标志用原子位，Timer.Stop 不幂等不可作判据；与流式路径同一套判定
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
		// 保留上游可读错误信息；最终错误信封由候选循环生成。
		m.upstreamMsg = upstreamMessage(b, "上游供应商返回错误")
		p.logger.Warn("上游非 2xx",
			"request_id", loggingRequestID(r.Context()),
			"status", resp.StatusCode,
			"error_detail", truncateForLog(b),
		)
		return true, class
	}

	// 2xx：全量缓冲，经适配器基座透传或桥接翻译改写，并抽取 usage 计量
	respBody, usageRaw, rerr := readAndRewrite(ad, resp)
	if rerr != nil {
		// 上游响应中断或桥接翻译失败：未向客户端写过任何字节，不写头直接归类，
		// 交候选循环决定换道或耗尽出信封；写 502 头再换道会产出 502 头加成功体的矛盾响应
		m.status, m.class = "error", failover.ClassMidStreamFail
		p.logger.Warn("非流式响应读取/改写失败", "request_id", loggingRequestID(r.Context()), "error_detail", rerr.Error())
		return true, failover.ClassMidStreamFail
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(respBody)

	if len(usageRaw) > 0 && string(usageRaw) != "null" {
		m.usageFrames = append(m.usageFrames, usageRaw)
	} else {
		m.est = true // usage 缺失转本地估算，计价需输入文本见 record
	}
	m.outBody = respBody
	return true, ""
}

// classifyTransportErr 区分客户端断连、首 token 超时和拨号失败。
func (p *Proxy) classifyTransportErr(w http.ResponseWriter, r *http.Request, err error, firstTokFired *atomic.Bool, m *meterState) (bool, failover.ErrorClass) {
	if r.Context().Err() != nil {
		// 客户端断连：静默退出，不换道不计失败，判定表口径 context_canceled
		m.status = "client_cancel"
		return true, failover.ClassContextCanceled
	}
	m.status = "error"
	if firstTokFired != nil && firstTokFired.Load() {
		// 首 token 超时，计时器触发取消即为此因
		p.logger.Warn("上游首 token 超时", "request_id", loggingRequestID(r.Context()), "error_detail", err.Error())
		m.class = failover.ClassTimeoutFirst
		return false, failover.ClassTimeoutFirst
	}
	var ne net.Error
	m.class = failover.ClassDialFail
	if errors.As(err, &ne) && ne.Timeout() {
		p.logger.Warn("上游拨号超时", "request_id", loggingRequestID(r.Context()), "error_detail", err.Error())
	} else {
		p.logger.Warn("上游连接失败", "request_id", loggingRequestID(r.Context()), "error_detail", err.Error())
	}
	return false, failover.ClassDialFail
}

// parseRetryAfter 解析 Retry-After 头的秒数形态；非法或缺失返回 0。
func parseRetryAfter(v string) time.Duration {
	secs, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || secs <= 0 {
		return 0
	}
	return time.Duration(secs) * time.Second
}

// truncateForLog 日志用 body 截断，防止大错误体刷爆日志。
func truncateForLog(b []byte) string {
	const limit = 2048
	if len(b) > limit {
		return string(b[:limit]) + "…(截断)"
	}
	return string(b)
}
