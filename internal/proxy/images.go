package proxy

import (
	"context"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/solvela/muniu/internal/adapter"
	"github.com/solvela/muniu/internal/auth"
	"github.com/solvela/muniu/internal/failover"
	"github.com/solvela/muniu/internal/keypool"
	"github.com/solvela/muniu/internal/metering"
	"github.com/solvela/muniu/internal/protocol"
)

const maxImageResponse = 64 << 20

// HandlerImages 同步生成不进入 chat 翻译或语义缓存。
func (p *Proxy) HandlerImages() http.Handler { return http.HandlerFunc(p.handleImages) }

func (p *Proxy) handleImages(w http.ResponseWriter, r *http.Request) {
	tw := &trackWriter{ResponseWriter: w}
	body, err := io.ReadAll(http.MaxBytesReader(tw, r.Body, 4<<20))
	if err != nil {
		p.writeError(tw, r, protocol.TypeBadRequest, "bad_request", "请求体过大或不可读")
		return
	}
	req, err := protocol.ParseImageRequest(body)
	if err != nil {
		p.writeError(tw, r, protocol.TypeBadRequest, "bad_request", "请求不符合 images 格式")
		return
	}
	topo := p.topo.Load()
	if p.plugins != nil {
		res, done := p.applyPlugins(tw, r, body)
		if done {
			return
		}
		if len(res.ModifiedBy) > 0 {
			req, err = protocol.ParseImageRequest(res.Body)
			if err != nil {
				p.writeError(tw, r, protocol.TypeBadRequest, "plugin_rejected", "插件改写后请求非法")
				return
			}
			p.markPluginModified(tw, r, res.ModifiedBy)
		}
	}
	cands, ok := topo.routes[req.Model]
	if !ok || len(cands) == 0 {
		p.writeError(tw, r, protocol.TypeBadRequest, "model_not_found", "模型别名不存在: "+req.Model)
		return
	}
	// key 级模型白名单闸：images 管道在插件后单点解析，此处校验即最终别名
	if !p.enforceModelAllowlist(tw, r, req.Model) {
		return
	}
	price := p.imagePricing.Get(req.Model)
	var cost int64
	currency := p.settlement
	if price != nil {
		currency = price.Currency
		cost, err = price.Cost(req.TierKey(), req.N)
		if err != nil {
			p.writeError(tw, r, protocol.TypeBadRequest, "bad_request", "生成成本超出整数范围")
			return
		}
		if info := auth.KeyFrom(r.Context()); info != nil && info.MaxRequestCostCredits > 0 && cost > info.MaxRequestCostCredits {
			p.writeError(tw, r, protocol.TypeQuotaExceeded, costBudgetExceededCode, "单请求成本预算已超限")
			return
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
			p.writeError(tw, r, classToType(last), "all_candidates_failed", "上游供应商暂时不可用,已尝试全部候选")
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
		handled, class := p.attemptImages(tw, r, topo.adapters[cand.Provider], cand, key, req, m)
		p.reportKeyOutcome(pool, key, class, handled, m)
		p.recordImages(r, req.Model, cand, m, cost, currency)
		if class == "" || tw.committed {
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

func (p *Proxy) attemptImages(w http.ResponseWriter, r *http.Request, ad adapter.Adapter, cand failover.Candidate, key *keypool.Key, req *protocol.ImageRequest, m *meterState) (bool, failover.ErrorClass) {
	fail := func(c failover.ErrorClass) (bool, failover.ErrorClass) {
		m.status, m.class = "error", c
		return true, c
	}
	prep, ok := ad.(adapter.ImagePreparer)
	if !ok {
		p.writeError(w, r, protocol.TypeBadRequest, "images_not_supported", "该供应商适配器不支持 images")
		return fail(failover.ClassBadRequest)
	}
	out, err := prep.PrepareImages(req, cand.Model, key.Value)
	if err != nil {
		p.writeError(w, r, protocol.TypeBadRequest, "bad_request", "出站请求构造失败")
		return fail(failover.ClassBadRequest)
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	var fired atomic.Bool
	timer := time.AfterFunc(p.timeouts.FirstToken, func() { fired.Store(true); cancel() })
	defer timer.Stop()
	upReq, err := p.outboundRequest(r, ctx, out)
	if err != nil {
		return fail(failover.ClassUnknown)
	}
	resp, err := p.client.Do(upReq)
	if err != nil {
		return p.classifyTransportErr(w, r, err, &fired, m)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, maxUpstreamErrBody))
		class := ad.Classify(resp.StatusCode, b)
		if class == failover.ClassRateLimited {
			m.retryAfter = parseRetryAfter(resp.Header.Get("Retry-After"))
		}
		m.upstreamMsg = upstreamMessage(b, "上游供应商返回错误")
		return fail(class)
	}
	// 多读一字节区别恰达上限和被截断的半包，完整读取前不提交任何客户端字节。
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxImageResponse+1))
	if err != nil && (r.Context().Err() != nil || fired.Load()) {
		return p.classifyTransportErr(w, r, err, &fired, m)
	}
	if err != nil || len(b) > maxImageResponse {
		return fail(failover.ClassMidStreamFail)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(resp.StatusCode)
	n, err := w.Write(b)
	if err != nil || n != len(b) {
		m.status = "client_cancel"
		m.class = failover.ClassContextCanceled
		return true, m.class
	}
	return true, ""
}

func (p *Proxy) recordImages(r *http.Request, alias string, cand failover.Candidate, m *meterState, cost int64, currency string) {
	if p.pipeline == nil {
		return
	}
	tenant, key := "", ""
	if info := auth.KeyFrom(r.Context()); info != nil {
		tenant, key = info.TenantID, info.ID
	}
	total := time.Since(m.start).Milliseconds()
	row := metering.NewRow(metering.RowParams{RequestID: loggingRequestID(r.Context()), TenantID: tenant, APIKeyID: key, ModelAlias: alias, UpstreamModel: cand.Model, Provider: cand.Provider, Status: m.status, ErrorClass: errorClassOf(m), TotalMs: total, Currency: currency})
	if m.status == "ok" {
		row.CostCredits = cost
	}
	UpstreamTotal.Inc(cand.Provider, errorClassOf(m))
	RequestsTotal.Inc(alias, m.status)
	RequestDuration.Observe(float64(total))
	p.pipeline.Submit(row)
}
