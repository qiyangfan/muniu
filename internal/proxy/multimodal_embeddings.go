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
	if p.plugins != nil {
		res, done := p.applyPlugins(tw, r, body)
		if done {
			return
		}
		if len(res.ModifiedBy) > 0 {
			req, err = protocol.ParseMultimodalEmbeddingRequest(res.Body)
			if err != nil {
				p.writeError(tw, r, protocol.TypeBadRequest, "plugin_rejected", "插件改写后的请求体不是合法原生请求")
				return
			}
			cands, ok = topo.routes[req.Model]
			if !ok || len(cands) == 0 {
				p.writeError(tw, r, protocol.TypeBadRequest, "model_not_found", "模型别名不存在: "+req.Model)
				return
			}
			if !p.enforceModelAllowlist(tw, r, req.Model) {
				return
			}
			p.markPluginModified(tw, r, res.ModifiedBy)
		}
	}

	fcs := make([]failover.Candidate, 0, len(cands))
	for _, c := range cands {
		ad := topo.adapters[c.Provider]
		prep, ok := ad.(adapter.MultimodalEmbeddingPreparer)
		if !ok || !prep.SupportsMultimodalEmbeddings(c.Model) {
			continue
		}
		fcs = append(fcs, failover.Candidate{Provider: c.Provider, Model: c.Model, Priority: c.Priority, Weight: c.Weight})
	}
	if len(fcs) == 0 {
		p.writeError(tw, r, protocol.TypeBadRequest, "multimodal_embeddings_not_supported", "该模型别名没有声明原生多模态向量能力的供应商候选")
		return
	}

	iter := failover.NewIterator(fcs, p.randIntn)
	var (
		lastClass    failover.ErrorClass
		lastUpstream *meterState
	)
	for {
		cand, ok := iter.Next()
		if !ok {
			if lastUpstream != nil {
				writeUpstreamResponse(tw, lastUpstream.upstreamStatus, lastUpstream.upstreamHeaders, lastUpstream.upstreamBody)
				return
			}
			p.writeError(tw, r, classToType(lastClass), "all_candidates_failed", "上游供应商暂时不可用,已尝试全部支持的候选")
			return
		}
		pool := topo.pools[cand.Provider]
		if pool == nil {
			continue
		}
		key := pool.Pick(time.Now())
		if key == nil {
			continue
		}
		m := &meterState{start: time.Now(), status: "ok"}
		ad := topo.adapters[cand.Provider]
		prep := ad.(adapter.MultimodalEmbeddingPreparer)
		handled, class := p.attemptMultimodalEmbeddings(tw, r, ad, prep, cand, key, req, m)
		p.reportKeyOutcome(pool, key, class, handled, m)
		if m.upstreamStatus != 0 {
			lastUpstream = m
		}
		if class == "" || tw.committed {
			return
		}
		switch failover.Decide(class, false, false) {
		case failover.ActionNextCandidate:
			lastClass = class
			continue
		case failover.ActionReturn:
			if m.upstreamStatus != 0 {
				writeUpstreamResponse(tw, m.upstreamStatus, m.upstreamHeaders, m.upstreamBody)
				return
			}
			p.writeError(tw, r, classToType(class), string(class), m.upstreamMsg)
			return
		case failover.ActionSilent:
			return
		default:
			return
		}
	}
}

func (p *Proxy) attemptMultimodalEmbeddings(w http.ResponseWriter, r *http.Request, ad adapter.Adapter,
	prep adapter.MultimodalEmbeddingPreparer, cand failover.Candidate, key *keypool.Key,
	req *protocol.MultimodalEmbeddingRequest, m *meterState) (bool, failover.ErrorClass) {
	m.stream = false
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
	acceptEncoding := strings.Join(r.Header.Values("Accept-Encoding"), ", ")
	if acceptEncoding == "" {
		// net/http 默认请求 gzip 并透明解压；明确 identity 才能保留原生响应头与 body。
		acceptEncoding = "identity"
	}
	upReq.Header.Set("Accept-Encoding", acceptEncoding)
	// 原生响应必须保留上游重定向；共享 HTTP client 默认会把 301/302 等改为后续响应。
	client := *p.client
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(upReq)
	if err != nil {
		return p.classifyTransportErr(w, r, err, &firstTokFired, m)
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxNonStreamBody+1))
	if err != nil || len(respBody) > maxNonStreamBody {
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
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		class := ad.Classify(resp.StatusCode, respBody)
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
	var response struct {
		Usage json.RawMessage `json:"usage"`
	}
	if err := json.Unmarshal(respBody, &response); err != nil || len(response.Usage) == 0 || string(response.Usage) == "null" {
		m.est = true
	} else {
		m.usageFrames = append(m.usageFrames, response.Usage)
	}
	m.outBody = respBody
	writeUpstreamResponse(w, resp.StatusCode, resp.Header, respBody)
	return true, ""
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
