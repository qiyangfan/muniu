// embeddings 入站通道将请求转发给支持 embeddings 的适配器，并沿用 chat 的计量流程。
package proxy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/solvela/muniu/internal/adapter"
	"github.com/solvela/muniu/internal/failover"
	"github.com/solvela/muniu/internal/keypool"
	"github.com/solvela/muniu/internal/protocol"
)

// maxEmbeddingBody embeddings 请求体上限 16MB，批量嵌入体积可较大。
const maxEmbeddingBody = 16 << 20

// HandlerEmbeddings 挂载到 mux 的 embeddings 入口。
func (p *Proxy) HandlerEmbeddings() http.Handler {
	return http.HandlerFunc(p.handleEmbeddings)
}

// handleEmbeddings 解析、路由、取 key、透传、计量；非流式单形态，无流式分支。
func (p *Proxy) handleEmbeddings(w http.ResponseWriter, r *http.Request) {
	tw := &trackWriter{ResponseWriter: w} // 提交追踪：已写出的响应绝不二次写出或换道
	body, err := io.ReadAll(http.MaxBytesReader(tw, r.Body, maxEmbeddingBody))
	if err != nil {
		p.writeError(tw, r, protocol.TypeBadRequest, "bad_request", "请求体过大或不可读")
		return
	}
	req, err := protocol.ParseEmbeddingRequest(body)
	if err != nil {
		p.writeError(tw, r, protocol.TypeBadRequest, "bad_request", "请求不符合 OpenAI embeddings 格式")
		return
	}
	topo := p.topo.Load() // 请求入口加载一次，全程同一快照
	cands, ok := topo.routes[req.Model]
	if !ok || len(cands) == 0 {
		p.writeError(tw, r, protocol.TypeBadRequest, "model_not_found", "模型别名不存在: "+req.Model)
		return
	}
	// key 级模型白名单闸之一：先于插件钩子，被拒请求不产生插件副作用
	if !p.enforceModelAllowlist(tw, r, req.Model) {
		return
	}
	// 插件请求体钩子与 chat 同链序：限流后、候选循环前，单次执行；
	// embeddings 输入同样承载业务载荷，脱敏必须在出站与计量之前
	if p.plugins != nil {
		res, done := p.applyPlugins(tw, r, body)
		if done {
			return
		}
		if len(res.ModifiedBy) > 0 {
			nreq, err := protocol.ParseEmbeddingRequest(res.Body)
			if err != nil {
				p.writeError(tw, r, protocol.TypeBadRequest, "plugin_rejected", "插件改写后的请求体不是合法请求")
				return
			}
			req = nreq
			if cands, ok = topo.routes[req.Model]; !ok || len(cands) == 0 {
				p.writeError(tw, r, protocol.TypeBadRequest, "model_not_found", "模型别名不存在: "+req.Model)
				return
			}
			// key 级模型白名单闸之二：插件改写 model 后以最终出站别名为准再校验
			if !p.enforceModelAllowlist(tw, r, req.Model) {
				return
			}
			p.markPluginModified(tw, r, res.ModifiedBy)
		}
	}
	fcs := make([]failover.Candidate, len(cands))
	for i, c := range cands {
		fcs[i] = failover.Candidate{Provider: c.Provider, Model: c.Model, Priority: c.Priority, Weight: c.Weight}
	}
	iter := failover.NewIterator(fcs, p.randIntn)
	var last failover.ErrorClass
	for {
		cand, ok := iter.Next()
		if !ok {
			if tw.committed {
				return
			}
			typ := classToType(last)
			p.writeError(tw, r, typ, "all_candidates_failed",
				"上游供应商暂时不可用,已尝试全部候选(最后错误: "+string(last)+")")
			return
		}
		pool := topo.pools[cand.Provider]
		key := pool.Pick(time.Now())
		if key == nil {
			p.logger.Warn("provider key 池不可用,跳过候选",
				"request_id", loggingRequestID(r.Context()), "provider", cand.Provider)
			continue
		}
		m := &meterState{start: time.Now(), status: "ok"}
		defer func() {
			p.record(r, req.Model, cand.Model, cand, string(req.Input), m)
		}()
		handled, class := p.attemptEmbeddings(tw, r, cand, key, req, m)
		if pool != nil {
			p.reportKeyOutcome(pool, key, class, handled, m)
		}
		if class == "" {
			return
		}
		// attempt 已对客户端产出早退信封：请求终结，绝不换道重写
		if tw.committed {
			return
		}
		switch failover.Decide(class, false, false) {
		case failover.ActionNextCandidate:
			last = class
			continue
		case failover.ActionReturn:
			p.writeError(tw, r, classToType(class), string(class), m.upstreamMsg)
			return
		default:
			return
		}
	}
}

// attemptEmbeddings 单候选非流式透传：2xx 整包回写并计量 usage；非 2xx 只归类不写信封，交耗尽路径输出。
func (p *Proxy) attemptEmbeddings(w http.ResponseWriter, r *http.Request, cand failover.Candidate, key *keypool.Key, req *protocol.EmbeddingRequest, m *meterState) (bool, failover.ErrorClass) {
	m.stream = false
	snap := p.topo.Load()
	adI, ok := snap.adapters[cand.Provider]
	if !ok {
		return true, failover.ClassUnknown
	}
	upstream := p.clientFor(snap, cand.Provider)
	prep, ok := adI.(adapter.EmbeddingPreparer)
	if !ok {
		// 适配器不支持 embeddings 时直接返回参数错误，不尝试换道。
		p.writeError(w, r, protocol.TypeBadRequest, "embeddings_not_supported", "该供应商适配器不支持 embeddings")
		return true, failover.ClassBadRequest
	}
	m.normalize = func(raw json.RawMessage) (protocol.Usage, error) {
		return adI.NormalizeUsage(raw)
	}
	out, err := prep.PrepareEmbeddings(req, cand.Model, key.Value)
	if err != nil {
		p.writeError(w, r, protocol.TypeBadRequest, "bad_request", "出站请求构造失败")
		return true, failover.ClassBadRequest
	}
	// 首 token 超时复用非流式 chat 的 FirstToken 档：embeddings 为非流式单形态，僵死上游不可无限悬挂
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
		class := adI.Classify(resp.StatusCode, b)

		m.creditEvidence = isCreditExhausted(resp.StatusCode, b)
		m.status, m.class = "error", class
		if class == failover.ClassRateLimited {
			m.retryAfter = parseRetryAfter(resp.Header.Get("Retry-After"))
		}
		m.upstreamMsg = upstreamMessage(b, "上游供应商返回错误")
		p.logger.Warn("embeddings 上游非 2xx",
			"request_id", loggingRequestID(r.Context()), "status", resp.StatusCode,
			"error_detail", truncateForLog(b))
		return true, class
	}

	outBody, usageRaw, rerr := readAndRewrite(adI, resp)
	if rerr != nil {
		// 未向客户端写过任何字节：不写头直接归类，交候选循环统一出口，与 chat 非流式一致。
		m.status, m.class = "error", failover.ClassMidStreamFail
		p.logger.Warn("embeddings 响应读取失败", "request_id", loggingRequestID(r.Context()), "error_detail", rerr.Error())
		return true, failover.ClassMidStreamFail
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(outBody)
	if len(usageRaw) > 0 && string(usageRaw) != "null" {
		m.usageFrames = append(m.usageFrames, usageRaw)
	} else {
		m.est = true
	}
	return true, ""
}

// readAndRewrite 读全量响应并经适配器改写/抽取 usage。
func readAndRewrite(ad adapter.Adapter, resp *http.Response) ([]byte, json.RawMessage, error) {
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxNonStreamBody))
	if err != nil {
		return nil, nil, err
	}
	return ad.RewriteResponse(b)
}
