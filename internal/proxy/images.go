package proxy

import (
	"context"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/solvela/muniu/internal/adapter"
	"github.com/solvela/muniu/internal/auth"
	"github.com/solvela/muniu/internal/config"
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
	req, done := p.imagePluginHook(tw, r, body, req)
	if done {
		return
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
	cost, currency, over := p.imagePreflight(tw, r, req)
	if over {
		return
	}
	p.runImageCandidates(tw, r, topo, req, cands, cost, currency)
}

// imagePluginHook 插件改写钩子：重解析后返回新请求，白名单在最终别名上另行校验。
func (p *Proxy) imagePluginHook(tw *trackWriter, r *http.Request, body []byte,
	req *protocol.ImageRequest) (*protocol.ImageRequest, bool) {
	if p.plugins == nil {
		return req, false
	}
	res, done := p.applyPlugins(tw, r, body)
	if done {
		return req, true
	}
	if len(res.ModifiedBy) == 0 {
		return req, false
	}
	nreq, err := protocol.ParseImageRequest(res.Body)
	if err != nil {
		p.writeError(tw, r, protocol.TypeBadRequest, "plugin_rejected", "插件改写后请求非法")
		return req, true
	}
	p.markPluginModified(tw, r, res.ModifiedBy)
	return nreq, false
}

// imagePreflight 生成成本预检：价目缺失按 0 成本，超 key 单请求预算即 402。
func (p *Proxy) imagePreflight(tw *trackWriter, r *http.Request, req *protocol.ImageRequest) (int64, string, bool) {
	price := p.imagePricing.Get(req.Model)
	if price == nil {
		return 0, p.settlement, false
	}
	cost, err := price.Cost(req.TierKey(), req.N)
	if err != nil {
		p.writeError(tw, r, protocol.TypeBadRequest, "bad_request", "生成成本超出整数范围")
		return 0, price.Currency, true
	}
	info := auth.KeyFrom(r.Context())
	if info != nil && info.MaxRequestCostCredits > 0 && cost > info.MaxRequestCostCredits {
		p.writeError(tw, r, protocol.TypeQuotaExceeded, costBudgetExceededCode, "单请求成本预算已超限")
		return cost, price.Currency, true
	}
	return cost, price.Currency, false
}

// runImageCandidates 候选循环：oauth 收窄、取 key、尝试、按 Decide 动作分支。
func (p *Proxy) runImageCandidates(tw *trackWriter, r *http.Request, topo *topologySnapshot,
	req *protocol.ImageRequest, cands []config.Candidate, cost int64, currency string) {
	fcs := make([]failover.Candidate, len(cands))
	for i, c := range cands {
		fcs[i] = failover.Candidate{Provider: c.Provider, Model: c.Model, Priority: c.Priority, Weight: c.Weight}
	}
	iter := failover.NewIterator(fcs, p.randIntn)
	var last failover.ErrorClass
	oauthSkipped := false // oauth 候选收窄:images lane 不可达,唯一跳因时显式信封
	otherSkipped := false // 池缺失/池耗尽等非 oauth 跳因
	for {
		cand, ok := iter.Next()
		if !ok {
			p.finishImageExhausted(tw, r, last, oauthSkipped, otherSkipped)
			return
		}
		if topo.authModes[cand.Provider] == adapter.AuthOAuth {
			oauthSkipped = true
			p.logger.Warn("oauth 渠道不支持 images lane,跳过候选",
				"request_id", loggingRequestID(r.Context()), "provider", cand.Provider)
			continue
		}
		pool := topo.pools[cand.Provider]
		if pool == nil {
			otherSkipped = true
			continue
		}
		key := pool.Pick(time.Now())
		if key == nil {
			otherSkipped = true
			continue
		}
		m := &meterState{start: time.Now(), status: "ok"}
		handled, class := p.attemptImages(tw, r, topo.adapters[cand.Provider], cand, key, req, m)
		p.reportKeyOutcome(pool, key, class, handled, m)
		p.recordImages(r, req.Model, cand, m, cost, currency)
		if stop, next := p.imageFailureExit(tw, r, class, m); stop {
			return
		} else if next {
			last = class
		}
	}
}

// imageFailureExit 非成功出口：next=true 换道继续，否则请求终结。
func (p *Proxy) imageFailureExit(tw *trackWriter, r *http.Request, class failover.ErrorClass,
	m *meterState) (stop, next bool) {
	if class == "" || tw.committed {
		return true, false
	}
	switch failover.Decide(class, false, false) {
	case failover.ActionNextCandidate:
		return false, true
	case failover.ActionReturn:
		p.writeError(tw, r, classToType(class), string(class), m.upstreamMsg)
	}
	return true, false
}

// finishImageExhausted 候选耗尽：oauth 唯一跳因时显式信封，否则统一 all_candidates_failed。
func (p *Proxy) finishImageExhausted(tw *trackWriter, r *http.Request, last failover.ErrorClass,
	oauthSkipped, otherSkipped bool) {
	if oauthSkipped && last == "" && !otherSkipped {
		p.writeError(tw, r, protocol.TypeBadRequest, oauthLaneUnsupportedCode,
			"该别名的候选均为 OAuth 订阅渠道,不支持 images lane,请为别名配置非 oauth 候选")
		return
	}
	p.writeError(tw, r, classToType(last), "all_candidates_failed", "上游供应商暂时不可用,已尝试全部候选")
}

// imageFail 统一失败出口：落分类并终结请求。
func imageFail(m *meterState, c failover.ErrorClass) (bool, failover.ErrorClass) {
	m.status, m.class = "error", c
	return true, c
}

func (p *Proxy) attemptImages(w http.ResponseWriter, r *http.Request, ad adapter.Adapter,
	cand failover.Candidate, key *keypool.Key, req *protocol.ImageRequest,
	m *meterState) (bool, failover.ErrorClass) {
	upstream := p.clientFor(p.topo.Load(), cand.Provider)
	prep, ok := ad.(adapter.ImagePreparer)
	if !ok {
		p.writeError(w, r, protocol.TypeBadRequest, "images_not_supported", "该供应商适配器不支持 images")
		return imageFail(m, failover.ClassBadRequest)
	}
	out, err := prep.PrepareImages(req, cand.Model, key.Value)
	if err != nil {
		p.writeError(w, r, protocol.TypeBadRequest, "bad_request", "出站请求构造失败")
		return imageFail(m, failover.ClassBadRequest)
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	var fired atomic.Bool
	timer := time.AfterFunc(p.timeouts.FirstToken, func() { fired.Store(true); cancel() })
	defer timer.Stop()
	upReq, err := p.outboundRequest(r, ctx, out)
	if err != nil {
		return imageFail(m, failover.ClassUnknown)
	}
	resp, err := upstream.Do(upReq)
	if err != nil {
		return p.classifyTransportErr(w, r, err, &fired, m)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return p.imageNon2xx(r, ad, m, resp)
	}
	return p.imageResponse(w, r, resp, &fired, m)
}

// imageResponse 读取、限长并回写图像响应；读失败区分客户端取消与截断。
func (p *Proxy) imageResponse(w http.ResponseWriter, r *http.Request, resp *http.Response,
	fired *atomic.Bool, m *meterState) (bool, failover.ErrorClass) {
	// 多读一字节区别恰达上限和被截断的半包，完整读取前不提交任何客户端字节。
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxImageResponse+1))
	if err != nil && (r.Context().Err() != nil || fired.Load()) {
		return p.classifyTransportErr(w, r, err, fired, m)
	}
	if err != nil || len(b) > maxImageResponse {
		return imageFail(m, failover.ClassMidStreamFail)
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

// imageNon2xx 图像上游非 2xx：归类并记录可读 message，信封由候选循环统一输出。
func (p *Proxy) imageNon2xx(r *http.Request, ad adapter.Adapter, m *meterState,
	resp *http.Response) (bool, failover.ErrorClass) {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, maxUpstreamErrBody))
	class := ad.Classify(resp.StatusCode, b)

	m.creditEvidence = isCreditExhausted(resp.StatusCode, b)
	if class == failover.ClassRateLimited {
		m.retryAfter = parseRetryAfter(resp.Header.Get("Retry-After"))
	}
	m.upstreamMsg = upstreamMessage(b, "上游供应商返回错误")
	m.status, m.class = "error", class
	return true, class
}

func (p *Proxy) recordImages(r *http.Request, alias string, cand failover.Candidate,
	m *meterState, cost int64, currency string) {
	if p.pipeline == nil {
		return
	}
	tenant, key := "", ""
	if info := auth.KeyFrom(r.Context()); info != nil {
		tenant, key = info.TenantID, info.ID
	}
	total := time.Since(m.start).Milliseconds()
	row := metering.NewRow(metering.RowParams{
		RequestID: loggingRequestID(r.Context()), TenantID: tenant, APIKeyID: key,
		ModelAlias: alias, UpstreamModel: cand.Model, Provider: cand.Provider,
		Status: m.status, ErrorClass: errorClassOf(m), TotalMs: total, Currency: currency,
	})
	if m.status == "ok" {
		row.CostCredits = cost
	}
	UpstreamTotal.Inc(cand.Provider, errorClassOf(m))
	RequestsTotal.Inc(alias, m.status)
	RequestDuration.Observe(float64(total))
	p.pipeline.Submit(row)
}
