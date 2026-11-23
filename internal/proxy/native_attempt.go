// 直通路径单候选执行：出站用原始体加鉴权注入；响应恒等透传，writer native 模式位；
// 计量与缓存拼包经 meta-only 读侧与翻译路径同口径；错误信封按路径分域：
// 不换道类原样透传，流内失败诚实终止不合成帧，直通不伪造协议形态。
package proxy

import (
	"context"
	"encoding/json"
	"errors"
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
func (p *Proxy) attemptNativeNonStream(w http.ResponseWriter, r *http.Request,
	upstream *http.Client, ad adapter.Adapter,
	np adapter.NativePreparer, out *adapter.Outbound, m *meterState, nat *inboundNative,
	cand failover.Candidate) (bool, failover.ErrorClass) {
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
		return p.nativeNon2xx(w, r, ad, m, resp)
	}

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxNonStreamBody))
	if err != nil {
		// 上游响应中断：未向客户端写过任何字节，交候选循环，与翻译路径同判定
		m.status, m.class = "error", failover.ClassMidStreamFail
		p.logger.Warn("直通非流式响应读取失败", "request_id", loggingRequestID(r.Context()), "error_detail", err.Error())
		return true, failover.ClassMidStreamFail
	}
	m.absorbNativeBody(nat, respBody)
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

// absorbNativeBody 非流式原生体计量：usage 原生形态归一并捕获 responses 响应 id，供亲和回写。
// Gemini 的原生键为 usageMetadata，其余协议为 usage。
func (m *meterState) absorbNativeBody(nat *inboundNative, respBody []byte) {
	var probe struct {
		ID            string          `json:"id"`
		Usage         json.RawMessage `json:"usage"`
		UsageMetadata json.RawMessage `json:"usageMetadata"` // gemini 原生形态
	}
	_ = json.Unmarshal(respBody, &probe)
	usage := probe.Usage
	if nat.proto == protocol.InboundGemini {
		usage = probe.UsageMetadata
	}
	if len(usage) > 0 && string(usage) != "null" {
		m.usageFrames = append(m.usageFrames, usage)
	} else {
		m.est = true
	}
	if nat.proto == protocol.InboundResponses && probe.ID != "" {
		m.nativeRespID = probe.ID
	}
}

// attemptNativeStream 直通流式：逐事件原字节透传，meta-only 读侧供计量与首 token 判定；
// 流内失败帧诚实透传后终止，不合成任何上游形态的帧，错误信封按路径分域。
func (p *Proxy) attemptNativeStream(w http.ResponseWriter, r *http.Request, upstream *http.Client, ad adapter.Adapter,
	np adapter.NativePreparer, out *adapter.Outbound, m *meterState, nat *inboundNative,
	cand failover.Candidate) (bool, failover.ErrorClass) {
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
		return p.nativeNon2xx(w, r, ad, m, resp)
	}

	evCh := startEventReader(resp.Body, opCtx)
	g := &firstTokGate{firstTokFired: &firstTokFired, firstTok: firstTok, chunkIdle: p.timeouts.ChunkIdle}
	ns := &nativeStreamState{nat: nat, cand: cand, np: np}
	for {
		select {
		case <-r.Context().Done():
			return p.streamClientGone(opCancel, m, g)
		case <-g.idleC:
			// 帧间超时即上游僵死：直通域内已透传字节如实终止，不合成非上游形态的帧
			opCancel()
			m.status, m.class = "error", failover.ClassMidStreamFail
			m.est = true
			return true, failover.ClassMidStreamFail
		case res, ok := <-evCh:
			done, handled, class := p.nativeStreamStep(w, r, m, g, ns, res, ok, opCancel)
			if done {
				return handled, class
			}
		}
	}
}

// nativeStreamState 直通流的请求级状态：协议、目标候选与适配器能力。
type nativeStreamState struct {
	nat  *inboundNative
	cand failover.Candidate
	np   adapter.NativePreparer
	wire []byte // 成功流的透传字节累计，缓存双存的原生侧，失败路径不消费
}

// nativeNon2xx 直通路径的非 2xx：归类并暂存原样错误体，不换道类由循环透传。
func (p *Proxy) nativeNon2xx(w http.ResponseWriter, r *http.Request, ad adapter.Adapter,
	m *meterState, resp *http.Response) (bool, failover.ErrorClass) {
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

// nativeStreamStep 处理单个读帧事件；done=false 表示继续 select 下一事件。
func (p *Proxy) nativeStreamStep(w http.ResponseWriter, r *http.Request, m *meterState, g *firstTokGate,
	ns *nativeStreamState, res eventResult, ok bool, opCancel context.CancelFunc) (bool, bool, failover.ErrorClass) {
	if !ok {
		handled, class := p.streamReaderClosed(r, m, g, opCancel)
		return true, handled, class
	}
	if res.err != nil {
		handled, class := p.nativeStreamReadErr(w, r, m, g, ns, res.err, opCancel)
		return true, handled, class
	}
	return p.nativeStreamFrame(w, r, m, g, ns, res, opCancel)
}

// nativeStreamReadErr 直通读帧出错：断连/未开闸可换道，已开闸如实终止已透传字节。
// 无终止标记协议（gemini）的干净关流等价终态：原字节已透传，不合成任何帧；
// 空内容响应(仅 finish/usage 帧)先放行缓冲再收尾，零事件空流仍按断流交候选循环。
func (p *Proxy) nativeStreamReadErr(w http.ResponseWriter, r *http.Request, m *meterState,
	g *firstTokGate, ns *nativeStreamState, err error, opCancel context.CancelFunc) (bool, failover.ErrorClass) {
	opCancel()
	if r.Context().Err() != nil {
		return p.streamClientGone(func() {}, m, g)
	}
	if errors.Is(err, io.EOF) {
		if scd, ok := ns.np.(adapter.StreamCloseDone); ok && scd.StreamCloseIsDone() {
			if p.finishNativeCleanClose(w, g, ns, m) {
				return true, ""
			}
		}
	}
	if !g.gateOpen {
		if g.firstTokFired.Load() {
			return m.streamTimeoutFirst()
		}
		return m.streamMidStreamFail() // 上游断流未过首 token，可换道
	}
	// 已过首 token：出口为如实终止已透传字节，不合成错误帧
	m.status, m.class = "error", failover.ClassMidStreamFail
	m.est = true
	return true, failover.ClassMidStreamFail
}

// finishNativeCleanClose 直通干净关流收尾：已开闸直接收尾；未开闸但已收到完整帧时
// 先放行缓冲（空内容响应首字节即终帧）；零事件返回 false，交候选循环按断流处理。
func (p *Proxy) finishNativeCleanClose(w http.ResponseWriter, g *firstTokGate,
	ns *nativeStreamState, m *meterState) bool {
	if !g.gateOpen {
		if len(g.pending) == 0 {
			return false
		}
		p.openGate(w)
		p.writeEvents(w, g.pending)
	}
	m.finishNativeStream(ns)
	return true
}

// nativeStreamFrame 直通正常读帧：原生事件解析、计量、预算闸与开闸/原字节透传。
func (p *Proxy) nativeStreamFrame(w http.ResponseWriter, r *http.Request, m *meterState, g *firstTokGate,
	ns *nativeStreamState, res eventResult, opCancel context.CancelFunc) (bool, bool, failover.ErrorClass) {
	internal, meta, ferr := ns.np.NativeStreamEvent([]byte(res.payload))
	if ferr != nil {
		return p.nativeStreamFailedFrame(w, r, m, g, res, ferr, opCancel)
	}
	// 计量与首 token 判定：内部 chunk 与翻译路径同口径消费，客户端恒收原字节
	src := nativeMeterSrc(internal, res.payload)
	m.accumulateDelta(src)
	m.absorbStreamUsage(src, meta)
	// 直通流预算闸边界：仅未开闸可拦，402 信封保证客户端零字节污染；
	// 已开闸后无原生协议错误帧合成机制，不中断，流内成本控制由翻译路径全量覆盖
	if m.nativeBudgetHit(g, meta) {
		handled, class := p.nativeBudgetAbort(w, r, m, opCancel)
		return true, handled, class
	}
	if ns.nat.proto == protocol.InboundResponses {
		if id := responsesEventID([]byte(res.payload)); id != "" {
			m.nativeRespID = id
		}
	}
	ns.wire = append(ns.wire, res.raw...)
	if meta.IsDone && !g.gateOpen {
		// 未开闸即收尾，无内容帧：放行缓冲原字节干净收尾
		p.openGate(w)
		p.writeEvents(w, g.pending)
		p.writeEvent(w, res.raw)
		m.finishNativeStream(ns)
		return true, true, ""
	}
	if !g.gateOpen {
		g.pending = append(g.pending, res.raw...)
		if meta.HasContent {
			g.open(p, w, m) // 首 token
		}
		return false, false, ""
	}
	return p.nativeOpenGateFrame(w, m, g, ns, res.raw, meta)
}

// nativeOpenGateFrame 已开闸帧：原字节透传、重置帧间计时，终态帧收尾。
func (p *Proxy) nativeOpenGateFrame(w http.ResponseWriter, m *meterState, g *firstTokGate,
	ns *nativeStreamState, raw []byte, meta adapter.EventMeta) (bool, bool, failover.ErrorClass) {
	p.writeEvent(w, raw)
	g.reset()
	return nativeDoneOrContinue(m, ns, meta)
}

// nativeBudgetHit 直通流预算闸:仅未开闸且非终态帧可拦。
func (m *meterState) nativeBudgetHit(g *firstTokGate, meta adapter.EventMeta) bool {
	return !g.gateOpen && !meta.IsDone && m.budgetExceeded()
}

// nativeDoneOrContinue 终态帧收尾,否则继续读帧。
func nativeDoneOrContinue(m *meterState, ns *nativeStreamState,
	meta adapter.EventMeta) (bool, bool, failover.ErrorClass) {
	if !meta.IsDone {
		return false, false, ""
	}
	m.finishNativeStream(ns)
	return true, true, ""
}

// nativeMeterSrc 计量消费体：适配器内部 chunk 优先，空则回退原始 data 载荷。
func nativeMeterSrc(internal []byte, payload string) []byte {
	if len(internal) == 0 {
		return []byte(payload)
	}
	return internal
}

// nativeStreamFailedFrame 原生流内失败帧：诚实透传该事件后终止，由上游裁决 wire，
// 直通不伪造协议形态、不换道。
func (p *Proxy) nativeStreamFailedFrame(w http.ResponseWriter, r *http.Request, m *meterState,
	g *firstTokGate, res eventResult, ferr error, opCancel context.CancelFunc) (bool, bool, failover.ErrorClass) {
	opCancel()
	p.logger.Warn("直通流内失败帧", "request_id", loggingRequestID(r.Context()), "error_detail", ferr.Error())
	if !g.gateOpen {
		p.openGate(w)
		p.writeEvents(w, g.pending)
	}
	p.writeEvent(w, res.raw)
	m.status, m.class = "error", failover.ClassMidStreamFail
	m.est = true
	return true, true, failover.ClassMidStreamFail
}

// nativeBudgetAbort 直通流未开闸的预算超限：402 信封写出，客户端零字节污染。
func (p *Proxy) nativeBudgetAbort(w http.ResponseWriter, r *http.Request, m *meterState,
	opCancel context.CancelFunc) (bool, failover.ErrorClass) {
	opCancel()
	m.status, m.class = "error", failover.ClassCostBudget
	if len(m.usageFrames) == 0 {
		m.est = true
	}
	p.writeError(w, r, protocol.TypeQuotaExceeded, costBudgetExceededCode, "单请求成本预算已超限")
	return true, failover.ClassCostBudget
}

// finishNativeStream 直通流正常收尾：usage 缺失置估算，产出缓存双存的原生侧与 canonical 侧。
func (m *meterState) finishNativeStream(ns *nativeStreamState) {
	if len(m.usageFrames) == 0 {
		m.est = true
	}
	m.outBody, m.canonicalBody = ns.wire, m.assembleStreamBody(ns.cand.Model)
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
// messages/gemini 走适配器翻译 RewriteResponse，responses 走受限逆映射；失败返回 nil，
// 仅降级为跨协议不可命中，不失败请求、不损同构回放。
func canonicalFromNative(ad adapter.Adapter, proto protocol.InboundProtocol, body []byte) []byte {
	switch proto {
	case protocol.InboundMessages, protocol.InboundGemini:
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
