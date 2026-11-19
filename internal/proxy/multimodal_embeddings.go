// 百炼原生多模态向量通道保留请求/响应 wire，只在出站前替换路由模型名。
package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/solvela/muniu/internal/adapter"
	"github.com/solvela/muniu/internal/config"
	"github.com/solvela/muniu/internal/failover"
	"github.com/solvela/muniu/internal/keypool"
	"github.com/solvela/muniu/internal/protocol"
)

// maxMultimodalEmbeddingBody 沿用 embeddings 请求体 16 MiB 上限。
const maxMultimodalEmbeddingBody = 16 << 20

// HandlerMultimodalEmbeddings 挂载百炼原生多模态向量入口。
func (p *Proxy) HandlerMultimodalEmbeddings() http.Handler {
	return http.HandlerFunc(p.handleMultimodalEmbeddings)
}

func (p *Proxy) handleMultimodalEmbeddings(w http.ResponseWriter, r *http.Request) {
	tw := &trackWriter{ResponseWriter: w}
	body, err := io.ReadAll(http.MaxBytesReader(tw, r.Body, maxMultimodalEmbeddingBody))
	if err != nil {
		p.writeError(tw, r, protocol.TypeBadRequest, "bad_request", "请求体过大或不可读")
		return
	}
	req, err := protocol.ParseMultimodalEmbeddingRequest(body)
	if err != nil {
		p.writeError(tw, r, protocol.TypeBadRequest, "bad_request", "请求不符合百炼原生多模态向量格式")
		return
	}
	topo := p.topo.Load()
	cands, ok := topo.routes[req.Model]
	if !ok || len(cands) == 0 {
		p.writeError(tw, r, protocol.TypeBadRequest, "model_not_found", "模型别名不存在: "+req.Model)
		return
	}
	if !p.enforceModelAllowlist(tw, r, req.Model) {
		return
	}
	req, cands, done := p.multimodalPluginHook(tw, r, topo, body, req, cands)
	if done {
		return
	}
	fcs := multimodalCandidates(topo, cands)
	if len(fcs) == 0 {
		p.writeError(tw, r, protocol.TypeBadRequest, "multimodal_embeddings_not_supported",
			"该模型别名没有声明原生多模态向量能力的供应商候选")
		return
	}
	p.runMultimodalCandidates(tw, r, topo, req, fcs)
}

// multimodalPluginHook 插件改写钩子：重解析、重路由与最终别名白名单复核。
func (p *Proxy) multimodalPluginHook(tw *trackWriter, r *http.Request, topo *topologySnapshot, body []byte,
	req *protocol.MultimodalEmbeddingRequest,
	cands []config.Candidate) (*protocol.MultimodalEmbeddingRequest, []config.Candidate, bool) {
	var nreq *protocol.MultimodalEmbeddingRequest
	failed := p.rewriteModelHook(tw, r, topo, body, "插件改写后的请求体不是合法原生请求",
		func(body []byte) (string, error) {
			parsed, err := protocol.ParseMultimodalEmbeddingRequest(body)
			if err != nil {
				return "", err
			}
			nreq = parsed
			return parsed.Model, nil
		},
		func(model string) bool {
			req, cands = nreq, topo.routes[model]
			return true
		})
	return req, cands, failed
}

// multimodalCandidates 仅保留声明原生多模态向量能力的候选，其余整路剔除。
func multimodalCandidates(topo *topologySnapshot, cands []config.Candidate) []failover.Candidate {
	fcs := make([]failover.Candidate, 0, len(cands))
	for _, c := range cands {
		prep, ok := topo.adapters[c.Provider].(adapter.MultimodalEmbeddingPreparer)
		if !ok || !prep.SupportsMultimodalEmbeddings(c.Model) {
			continue
		}
		fcs = append(fcs, failover.Candidate{Provider: c.Provider, Model: c.Model, Priority: c.Priority, Weight: c.Weight})
	}
	return fcs
}

// runMultimodalCandidates 候选循环：oauth 收窄、取 key、尝试、按 Decide 动作分支。
func (p *Proxy) runMultimodalCandidates(tw *trackWriter, r *http.Request, topo *topologySnapshot,
	req *protocol.MultimodalEmbeddingRequest, fcs []failover.Candidate) {
	iter := failover.NewIterator(fcs, p.randIntn)
	var (
		lastClass    failover.ErrorClass
		lastUpstream *meterState
		oauthSkipped bool // oauth 候选收窄:多模态 lane 不可达,唯一跳因时显式信封
		otherSkipped bool // 池缺失/池耗尽等非 oauth 跳因
	)
	for {
		cand, ok := iter.Next()
		if !ok {
			p.finishMultimodalExhausted(tw, r, lastClass, lastUpstream, oauthSkipped, otherSkipped)
			return
		}
		if topo.authModes[cand.Provider] == adapter.AuthOAuth {
			oauthSkipped = true
			p.logger.Warn("oauth 渠道不支持多模态 embeddings lane,跳过候选",
				"request_id", loggingRequestID(r.Context()), "provider", cand.Provider)
			continue
		}
		key := p.pickMultimodalKey(topo, cand, &otherSkipped)
		if key == nil {
			continue
		}
		m := &meterState{start: time.Now(), status: "ok"}
		ad := topo.adapters[cand.Provider]
		prep := ad.(adapter.MultimodalEmbeddingPreparer)
		handled, class := p.attemptMultimodalEmbeddings(tw, r, ad, prep, cand, key, req, m)
		p.reportKeyOutcome(topo.pools[cand.Provider], key, class, handled, m)
		if m.upstreamStatus != 0 {
			lastUpstream = m
		}
		if stop, next := p.multimodalFailureExit(tw, r, class, m, lastUpstream); stop {
			return
		} else if next {
			lastClass = class
		}
	}
}

// pickMultimodalKey 取候选 key；池缺失或池内无可用 key 登记非 oauth 跳因。
func (p *Proxy) pickMultimodalKey(topo *topologySnapshot, cand failover.Candidate, otherSkipped *bool) *keypool.Key {
	pool := topo.pools[cand.Provider]
	if pool == nil {
		*otherSkipped = true
		return nil
	}
	key := pool.Pick(time.Now())
	if key == nil {
		*otherSkipped = true
	}
	return key
}

// multimodalFailureExit 非成功出口：next=true 换道继续，否则请求终结。
func (p *Proxy) multimodalFailureExit(tw *trackWriter, r *http.Request, class failover.ErrorClass,
	m *meterState, lastUpstream *meterState) (stop, next bool) {
	if class == "" || tw.committed {
		return true, false
	}
	switch failover.Decide(class, false, false) {
	case failover.ActionNextCandidate:
		return false, true
	case failover.ActionReturn:
		if m.upstreamStatus != 0 {
			writeUpstreamResponse(tw, m.upstreamStatus, m.upstreamHeaders, m.upstreamBody)
			return true, false
		}
		p.writeError(tw, r, classToType(class), string(class), m.upstreamMsg)
	}
	_ = lastUpstream
	return true, false
}

// finishMultimodalExhausted 候选耗尽：最后上游响应优先原样透传，其次 oauth 唯一跳因显式信封。
func (p *Proxy) finishMultimodalExhausted(tw *trackWriter, r *http.Request, lastClass failover.ErrorClass,
	lastUpstream *meterState, oauthSkipped, otherSkipped bool) {
	if tw.committed {
		return
	}
	if lastUpstream != nil {
		writeUpstreamResponse(tw, lastUpstream.upstreamStatus, lastUpstream.upstreamHeaders, lastUpstream.upstreamBody)
		return
	}
	if oauthSkipped && lastClass == "" && !otherSkipped {
		p.writeError(tw, r, protocol.TypeBadRequest, oauthLaneUnsupportedCode,
			"该别名的候选均为 OAuth 订阅渠道,不支持多模态 embeddings lane,请为别名配置非 oauth 候选")
		return
	}
	p.writeError(tw, r, classToType(lastClass), "all_candidates_failed", "上游供应商暂时不可用,已尝试全部支持的候选")
}

func (p *Proxy) attemptMultimodalEmbeddings(w http.ResponseWriter, r *http.Request, ad adapter.Adapter,
	prep adapter.MultimodalEmbeddingPreparer, cand failover.Candidate, key *keypool.Key,
	req *protocol.MultimodalEmbeddingRequest, m *meterState) (bool, failover.ErrorClass) {
	m.stream = false
	upstream := p.clientFor(p.topo.Load(), cand.Provider)
	m.normalize = func(raw json.RawMessage) (protocol.Usage, error) {
		return prep.NormalizeMultimodalEmbeddingUsage(raw, cand.Model)
	}
	defer func() { p.record(r, req.Model, cand.Model, cand, string(req.Raw), m) }()
	out, err := prep.PrepareMultimodalEmbeddings(req, cand.Model, key.Value)
	if err != nil {
		p.writeError(w, r, protocol.TypeBadRequest, "bad_request", "出站请求构造失败")
		return true, failover.ClassBadRequest
	}
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
	upReq.Header.Set("Accept-Encoding", nativeAcceptEncoding(r))
	// 原生响应必须保留上游重定向；共享 HTTP client 默认会把 301/302 等改为后续响应。
	client := *upstream
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(upReq)
	if err != nil {
		return p.classifyTransportErr(w, r, err, &firstTokFired, m)
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxNonStreamBody+1))
	if err != nil || len(respBody) > maxNonStreamBody {
		return p.multimodalReadErr(r, m, err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return p.multimodalNon2xx(r, ad, m, resp, respBody)
	}
	usage, hasUsage := multimodalUsage(respBody)
	if hasUsage {
		m.usageFrames = append(m.usageFrames, usage)
	} else {
		m.est = true
	}
	m.outBody = respBody
	writeUpstreamResponse(w, resp.StatusCode, resp.Header, respBody)
	return true, ""
}

// multimodalUsage 提取原生响应 usage；缺失或为 null 时要求按估算计量。
func multimodalUsage(body []byte) (json.RawMessage, bool) {
	var response struct {
		Usage json.RawMessage `json:"usage"`
	}
	if err := json.Unmarshal(body, &response); err != nil || len(response.Usage) == 0 ||
		string(response.Usage) == "null" {
		return nil, false
	}
	return response.Usage, true
}

// nativeAcceptEncoding 保留客户端协商结果；缺省 identity：net/http 默认请求 gzip 并透明解压，
// 明确 identity 才能保留原生响应头与 body。
func nativeAcceptEncoding(r *http.Request) string {
	if v := strings.Join(r.Header.Values("Accept-Encoding"), ", "); v != "" {
		return v
	}
	return "identity"
}

// multimodalReadErr 原生多模态向量响应读取失败：断连静默退出，超限或读错归中途断流。
func (p *Proxy) multimodalReadErr(r *http.Request, m *meterState, err error) (bool, failover.ErrorClass) {
	if err == nil {
		err = fmt.Errorf("响应体超过 %d 字节上限", maxNonStreamBody)
	}
	if r.Context().Err() != nil {
		m.status = "client_cancel"
		return true, failover.ClassContextCanceled
	}
	m.status, m.class = "error", failover.ClassMidStreamFail
	p.logger.Warn("原生多模态向量响应读取失败", "request_id", loggingRequestID(r.Context()), "error_detail", err.Error())
	return false, failover.ClassMidStreamFail
}

// multimodalNon2xx 原生多模态向量非 2xx：暂存原样信封，交候选循环透传或归类。
func (p *Proxy) multimodalNon2xx(r *http.Request, ad adapter.Adapter,
	m *meterState, resp *http.Response, respBody []byte) (bool, failover.ErrorClass) {
	class := ad.Classify(resp.StatusCode, respBody)
	m.creditEvidence = isCreditExhausted(resp.StatusCode, respBody)
	m.status, m.class = "error", class
	m.upstreamBody, m.upstreamStatus = respBody, resp.StatusCode
	m.upstreamHeaders = resp.Header.Clone()
	m.upstreamCT = resp.Header.Get("Content-Type")
	m.upstreamMsg = upstreamMessage(respBody, "上游供应商返回错误")
	if class == failover.ClassRateLimited {
		m.retryAfter = parseRetryAfter(resp.Header.Get("Retry-After"))
	}
	p.logger.Warn("原生多模态向量上游非 2xx", "request_id", loggingRequestID(r.Context()),
		"status", resp.StatusCode, "error_detail", truncateForLog(respBody))
	return true, class
}

func writeUpstreamResponse(w http.ResponseWriter, status int, headers http.Header, body []byte) {
	copyEndToEndHeaders(w.Header(), headers)
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func copyEndToEndHeaders(dst, src http.Header) {
	hopByHop := map[string]bool{
		"Connection": true, "Keep-Alive": true, "Proxy-Authenticate": true,
		"Proxy-Authorization": true, "Proxy-Connection": true, "Te": true,
		"Trailer": true, "Transfer-Encoding": true, "Upgrade": true,
	}
	for _, value := range src.Values("Connection") {
		for _, token := range strings.Split(value, ",") {
			hopByHop[http.CanonicalHeaderKey(strings.TrimSpace(token))] = true
		}
	}
	for name, values := range src {
		if hopByHop[http.CanonicalHeaderKey(name)] {
			continue
		}
		for _, value := range values {
			dst.Add(name, value)
		}
	}
}
