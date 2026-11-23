// Package proxy 编排请求生命周期，并协调适配器、计量、限流等数据面组件。
package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/solvela/muniu/internal/adapter"
	"github.com/solvela/muniu/internal/alerting"
	"github.com/solvela/muniu/internal/auth"
	"github.com/solvela/muniu/internal/config"
	"github.com/solvela/muniu/internal/failover"
	"github.com/solvela/muniu/internal/health"
	"github.com/solvela/muniu/internal/keypool"
	"github.com/solvela/muniu/internal/logging"
	"github.com/solvela/muniu/internal/metering"
	"github.com/solvela/muniu/internal/plugin"
	"github.com/solvela/muniu/internal/protocol"
	"github.com/solvela/muniu/internal/ratelimit"
	"github.com/solvela/muniu/internal/semantic"
)

// maxInboundBody 入站请求体上限 32MB，防恶意大包，模型上下文远小于此。
const maxInboundBody = 32 << 20

// Timeouts 三层超时的运行态形态。
type Timeouts struct {
	Dial       time.Duration
	FirstToken time.Duration
	ChunkIdle  time.Duration
}

const providerCircuitOpenCode = "provider_circuit_open"

// oauthLaneUnsupportedCode oauth lane 收窄的显式拒绝码:候选均为订阅渠道而入站 lane 不支持。
const oauthLaneUnsupportedCode = "oauth_lane_unsupported"

// oauthSkippedLane 收窄判定:nat 非空且非 messages 入站(responses/gemini)命中 oauth 渠道即跳过;
// 无原生上下文时以 lane 标记兜底;chat(nat 空且无 lane 标记)与 messages 恒放行。
func oauthSkippedLane(r *http.Request, nat *inboundNative, authModes map[string]string, provider string) bool {
	if nat != nil {
		return nat.proto != protocol.InboundMessages &&
			authModes[provider] == adapter.AuthOAuth
	}
	return laneFrom(r) == protocol.InboundGemini &&
		authModes[provider] == adapter.AuthOAuth
}

// Config 代理装配参数，由 New 消费，结构化形态避免参数膨胀。
type Config struct {
	Registry         *adapter.Registry
	Providers        []config.Provider // provider 定义，拓扑快照的池保留基准
	Routes           map[string][]config.Candidate
	Settlement       string // 网关本位币；空为 CNY，无价目行也须同币种。
	ImagePricing     *metering.ImagePriceDirectory
	Pricing          *metering.PricingDirectory // 价目目录，动态改价由管理面原子替换，转发只读；nil 表示成本记 0
	Timeouts         Timeouts
	Pipeline         *metering.Pipeline                 // 计量管道；nil 表示不计量，纯转发场景
	Pools            map[string]*keypool.Pool           // provider 到 key 池；缺池则该 provider 无法出站
	TPM              ratelimit.TPMRecorder              // TPM 记账回填，TPM 或 redis 后端，前置判定在 Gate 中间件
	Cache            *semantic.Cache                    // 语义缓存；nil 表示关闭
	TenantSwitch     func(context.Context, string) bool // 租户语义缓存开关，装配层提供 DB 查询闭包
	Capabilities     []config.ModelCapability           // 模型能力目录；空表示全部零干预透传
	StrictGovernance bool                               // true 表示命中能力约束即 400，缺省宽松放行并留痕
	// InheritProxy 控制面拓扑来源标记:控制面载荷不含 proxy_url 字段,置 true 时入站声明为空
	// 的渠道沿用旧快照代理声明(防静默摘除);配置文件源(SIGHUP/启动)必须保持 false——
	// 用户从 yaml 删除 proxy_url 是显式清空,沿用会吞掉变更(评审 R1)。
	InheritProxy bool
	// OAuth 实例的 oauth 端点声明(来自 yaml oauth 段,不随控制面拓扑下发):ReplaceTopology
	// 的 auth 共享校验以此判定「auth=oauth 渠道需 oauth 段完整」。
	OAuth    config.OAuthCfg
	Plugins  *plugin.Registry   // WASM 插件注册表；nil 表示插件禁用，主路径零开销
	Alerts   *alerting.Notifier // 告警通知器；nil 表示关闭，Emit 零开销
	Health   *health.Manager    // 渠道健康熔断管理器；nil = 关闭,候选域零收缩
	RandIntn func(int) int      // 候选起点随机源；nil 用全局随机，测试可注入
	Logger   *slog.Logger
}

// Proxy 编排器：注册表、拓扑快照、三层超时与计量管道；拓扑快照含路由、池、适配器、能力目录，支持原子热替换。
type Proxy struct {
	registry      *adapter.Registry                // 启动装配容器，转发路径不再读，adapter 随拓扑快照
	topo          atomic.Pointer[topologySnapshot] // 请求入口 Load 一次，全程同快照，换道视图一致
	settlement    string
	imagePricing  *metering.ImagePriceDirectory
	pricing       *metering.PricingDirectory
	timeouts      Timeouts
	pipeline      *metering.Pipeline
	tpm           ratelimit.TPMRecorder
	cache         *semantic.Cache
	tenantSw      func(context.Context, string) bool
	strictGov     bool
	plugins       *plugin.Registry
	alerts        *alerting.Notifier
	health        *health.Manager
	randIntn      func(int) int
	logger        *slog.Logger
	oauthCfg      config.OAuthCfg // 实例 oauth 段(拓扑守门的 oauth 完整性判定基准)
	client        *http.Client
	affinity      *affinityTable      // 有状态粘性路由亲和表，进程内 TTL+LRU
	cacheAffinity *cacheAffinityTable // 会话缓存亲和表(ADR-0020),独立生命周期
}

// New 构造代理：路由候选按 priority 升序排序；HTTP 客户端带拨号层超时与连接池。
func New(cfg Config) *Proxy {
	transport := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		DialContext:         (&net.Dialer{Timeout: cfg.Timeouts.Dial, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout: cfg.Timeouts.Dial,
		// 自定义 DialContext 会关闭 net/http 的自动 h2 升级，须显式开启，上游 HTTPS 依赖多路复用
		ForceAttemptHTTP2:   true,
		MaxIdleConns:        256,
		MaxIdleConnsPerHost: 64,
		IdleConnTimeout:     90 * time.Second,
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	randIntn := cfg.RandIntn
	if randIntn == nil {
		randIntn = func(n int) int { return rand.IntN(n) } //nolint:gosec // 退避抖动,非安全随机
	}
	settlement := cfg.Settlement
	if settlement == "" {
		settlement = "CNY"
	}
	p := &Proxy{
		settlement:    settlement,
		registry:      cfg.Registry,
		pricing:       cfg.Pricing,
		imagePricing:  cfg.ImagePricing,
		timeouts:      cfg.Timeouts,
		pipeline:      cfg.Pipeline,
		tpm:           cfg.TPM,
		cache:         cfg.Cache,
		tenantSw:      cfg.TenantSwitch,
		plugins:       cfg.Plugins,
		alerts:        cfg.Alerts,
		health:        cfg.Health,
		strictGov:     cfg.StrictGovernance,
		oauthCfg:      cfg.OAuth,
		randIntn:      randIntn,
		logger:        logger,
		client:        &http.Client{Transport: transport},
		affinity:      newAffinityTable(),
		cacheAffinity: newCacheAffinityTable(),
	}
	p.topo.Store(&topologySnapshot{
		providers:   append([]config.Provider(nil), cfg.Providers...),
		routes:      sortRoutes(cfg.Routes),
		pools:       cfg.Pools,
		adapters:    initialAdapters(cfg),
		clients:     initialClients(cfg, logger),
		caps:        buildCaps(cfg.Capabilities),
		nativeResps: buildNativeResps(cfg.Providers),
		authModes:   buildAuthModes(cfg.Providers), // 与 ReplaceTopology 同源:引导快照不落表则启动路径收窄失灵
		loadedAt:    time.Now(),
	})
	return p
}

// clientFor 候选出站 client:声明代理的渠道用独立 client(固定 Proxy,对 env 免疫),
// 其余复用默认 client —— 零声明部署连实例都不新增,行为与现状逐字节一致。
func (p *Proxy) clientFor(snap *topologySnapshot, provider string) *http.Client {
	if cl, ok := snap.clients[provider]; ok {
		return cl
	}
	return p.client
}

// initialClients 构建声明代理渠道的出站 client;解析失败降级默认 client 并告警
// (config.Load 已校验形态,此处兜底控制面拓扑等旁路来源)。
func initialClients(cfg Config, logger *slog.Logger) map[string]*http.Client {
	var out map[string]*http.Client
	for _, prov := range cfg.Providers {
		if prov.ProxyURL == "" {
			continue
		}
		cl, err := ProxiedClient(prov.ProxyURL, cfg.Timeouts.Dial)
		if err != nil {
			logger.Warn("渠道出站代理非法,降级默认出站", "provider", prov.Name,
				"proxy_url", redactProxyURL(prov.ProxyURL), "error", err.Error())
			continue
		}
		if out == nil {
			out = make(map[string]*http.Client, len(cfg.Providers))
		}
		out[prov.Name] = cl
	}
	return out
}

// redactProxyURL 日志脱敏:代理 URL 允许内嵌 userinfo 凭证,任何日志/错误面不得回显原文。
func redactProxyURL(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		return u.Redacted()
	}
	return "(unparseable)"
}

// ProxiedClient 固定代理出站 client:Proxy 恒为声明 URL,对环境变量免疫(显式声明即完整接管,
// 不叠加「代理失败回落 env」语义——叠加不可测)。探针与余额出站复用同一构造。
func ProxiedClient(raw string, dialTimeout time.Duration) (*http.Client, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("代理 URL 非法: %q(须为 http/https 绝对 URL)", redactProxyURL(raw))
	}
	transport := &http.Transport{
		Proxy:               http.ProxyURL(u),
		DialContext:         (&net.Dialer{Timeout: dialTimeout, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout: dialTimeout,
		ForceAttemptHTTP2:   true,
		MaxIdleConns:        256,
		MaxIdleConnsPerHost: 64,
		IdleConnTimeout:     90 * time.Second,
	}
	return &http.Client{Transport: transport}, nil
}

// initialAdapters 启动期从装配注册表提取各 provider 的适配器，构成拓扑快照初始形态；
// 转发路径此后只读快照，热替换失败回滚时新旧适配器不混用。
// 兼容未传 Providers 的调用方：从路由候选引用的 provider 集合提取。
func initialAdapters(cfg Config) map[string]adapter.Adapter {
	names := map[string]bool{}
	for _, prov := range cfg.Providers {
		names[prov.Name] = true
	}
	for _, cands := range cfg.Routes {
		for _, c := range cands {
			names[c.Provider] = true
		}
	}
	out := make(map[string]adapter.Adapter, len(names))
	for name := range names {
		if cfg.Registry == nil {
			continue
		}
		if ad, ok := cfg.Registry.Get(name); ok {
			out[name] = ad
		}
	}
	return out
}

// Handler 返回挂载到 mux 的入口(POST /v1/chat/completions)。
func (p *Proxy) Handler() http.Handler {
	return http.HandlerFunc(p.handleChat)
}

// cacheTenantEnabled 查租户语义缓存开关，装配层注入 DB 查询闭包。
func (p *Proxy) cacheTenantEnabled(ctx context.Context, tenantID string) bool {
	if p.tenantSw == nil {
		return false
	}
	return p.tenantSw(ctx, tenantID)
}

// trackWriter 记录响应是否已向客户端提交，写过头或体都算。候选循环据此终结：
// attempt 内部已写出的信封即早退路径，或断言出口之后，绝不允许再次换道写出，
// 否则客户端会收到双信封拼接或 500 头加成功体的语义矛盾响应。
type trackWriter struct {
	http.ResponseWriter
	committed bool
}

func (t *trackWriter) WriteHeader(code int) {
	t.committed = true
	t.ResponseWriter.WriteHeader(code)
}

func (t *trackWriter) Write(p []byte) (int, error) {
	t.committed = true
	return t.ResponseWriter.Write(p)
}

// Flush 转发底层 Flusher，SSE 逐事件冲刷依赖此能力，勿让本包装截断能力探测。
func (t *trackWriter) Flush() {
	if f, ok := t.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// setNativeMode 穿透直通模式位到协议 wrapper，候选循环保证只在首字节前切换。
func (t *trackWriter) setNativeMode(n bool) {
	if pw, ok := t.ResponseWriter.(pathModeWriter); ok {
		pw.setNativeMode(n)
	}
}

// Unwrap 支持 http.ResponseController 穿透包装。
func (t *trackWriter) Unwrap() http.ResponseWriter { return t.ResponseWriter }

// handleChat 生命周期：解析校验、路由、候选循环。
// 直通路径：入站原生上下文经 request context 传入，每 attempt 判定直通或翻译路径；
// 失格请求惰性升格，有状态请求粘性路由。
func (p *Proxy) handleChat(w http.ResponseWriter, r *http.Request) {
	tw := &trackWriter{ResponseWriter: w} // 提交追踪：已写出的响应绝不二次写出或换道
	body, err := io.ReadAll(http.MaxBytesReader(tw, r.Body, maxInboundBody))
	if err != nil {
		p.writeError(tw, r, protocol.TypeBadRequest, "bad_request", "请求体过大或不可读")
		return
	}
	nat := inboundFrom(r) // chat 入站为 nil，以下所有直通分支短路，不改变既有行为
	req, parsed := p.parseChatInbound(tw, r, nat, body)
	if !parsed {
		return
	}
	topo := p.topo.Load() // 请求入口加载一次，全程同一快照，换道候选视图一致
	cands, ok := topo.routes[req.Model]
	if !ok || len(cands) == 0 {
		p.writeError(tw, r, protocol.TypeBadRequest, "model_not_found", "模型别名不存在: "+req.Model)
		return
	}
	// key 级模型白名单闸之一：fail fast 于插件钩子前，被拒请求不产生插件副作用
	if !p.enforceModelAllowlist(tw, r, req.Model) {
		return
	}

	cands, req, pluginModified, done := p.applyChatPreHooks(tw, r, body, topo, nat, req, cands)
	if done {
		return
	}

	cph, done := p.chatCacheLookup(tw, r, nat, req)
	if done {
		return
	}

	requestPrices, requestBudget, over := p.chatBudgetPreflight(tw, r, nat, req)
	if over {
		return
	}

	// 身份先于 iterator 解析:stick 健康时作为组内起点提示
	sk := p.resolveStick(r, nat, req)
	iter := failover.NewIteratorWithStick(chatCandidates(cands), p.randIntn, sk.provider)
	// 桥接工具多轮边界：多轮中途流量对翻译路径的桥接候选不可服务，
	// 直通候选正常服务，不受此边界约束
	midLoop := chatToolLoopInflight(req)
	if p.health != nil {
		p.health.AliasRequest(req.Model) // 换道率分母:每请求一次
	}
	lc := &chatLoopCtrl{tw: tw, r: r, topo: topo, req: req, nat: nat, midLoop: midLoop,
		pluginModified: pluginModified, prices: requestPrices, budget: requestBudget}
	p.runChatCandidates(iter, lc, &chatLoopState{}, &sk, cph)
}

// chatBudgetPreflight 价目在请求入口选定；预检、候选尝试与落账均使用这份不可变快照。
// over 表示预检已写出超限信封。
func (p *Proxy) chatBudgetPreflight(tw *trackWriter, r *http.Request, nat *inboundNative,
	req *protocol.ChatRequest) (*metering.Pricing, int64, bool) {
	var prices *metering.Pricing
	if pt := p.pricing.Get(req.Model); pt != nil {
		pr := pt.At(time.Now())
		prices = &pr
	}
	var budget int64
	if info := auth.KeyFrom(r.Context()); info != nil {
		budget = info.MaxRequestCostCredits
	}
	if budget <= 0 || (nat != nil && nat.ineligCode != "") {
		return prices, budget, false
	}
	if msg, over := preflightBudgetExceeded(req, budget, prices); over {
		p.writeError(tw, r, protocol.TypeQuotaExceeded, costBudgetExceededCode, msg)
		return prices, budget, true
	}
	return prices, budget, false
}

// parseChatInbound 解析入站体为 canonical chat 请求；失格请求给最小路由载体。
func (p *Proxy) parseChatInbound(tw *trackWriter, r *http.Request, nat *inboundNative,
	body []byte) (*protocol.ChatRequest, bool) {
	if nat != nil && nat.ineligCode != "" {
		// 翻译失格：canonical 不可得，最小路由载体，治理与缓存不可评估，直通专属语义
		return &protocol.ChatRequest{Model: nat.model, Stream: nat.stream}, true
	}
	req, err := protocol.ParseChatRequest(body)
	if err != nil {
		p.writeError(tw, r, protocol.TypeBadRequest, "bad_request", "请求不符合 OpenAI Chat Completions 格式")
		return nil, false
	}
	return req, true
}

// chatLoopCtrl 候选循环的请求级不可变上下文。
type chatLoopCtrl struct {
	tw             *trackWriter
	r              *http.Request
	topo           *topologySnapshot
	req            *protocol.ChatRequest
	nat            *inboundNative
	midLoop        bool
	pluginModified bool
	prices         *metering.Pricing
	budget         int64
}

// runChatCandidates 候选循环：跳过、路径判定、取 key、执行、按 Decide 动作分支。
func (p *Proxy) runChatCandidates(iter failover.Iterator, lc *chatLoopCtrl, st *chatLoopState,
	sk *stickState, cph *cachePhase) {
	for {
		cand, ok := iter.Next()
		if !ok {
			p.finishChatExhausted(lc.tw, lc.r, lc.req, lc.nat, st, sk)
			return
		}
		if kind, skip := p.skipChatCandidate(lc.r, lc.topo, lc.nat, cand, lc.midLoop); skip {
			st.noteSkip(kind)
			continue
		}
		// 每 attempt 直通或翻译路径判定，决策点唯一，chat 入站恒翻译
		path := decideNativePath(lc.nat, lc.topo.adapters, lc.topo.nativeResps, cand, lc.pluginModified)
		if p.rejectBeforeOutbound(lc.tw, lc.r, lc.nat, path, sk) {
			return
		}
		key := p.pickChatKey(lc.r, lc.topo, cand, sk, st)
		if key == nil {
			continue
		}
		m := &meterState{
			start: time.Now(), status: "ok",
			priceSnapshot: lc.prices, priceSnapshotTaken: true,
			budget: lc.budget, budgetPrices: lc.prices,
		}
		handled, class := p.attempt(lc.tw, lc.r, lc.topo, lc.req, lc.nat, path, cand, key, m)
		if pool := lc.topo.pools[cand.Provider]; pool != nil {
			p.reportKeyOutcome(pool, key, class, handled, m)
		}
		if class == "" {
			p.finishChatSuccess(lc.r, lc.nat, cand, key, m, sk, cph, lc.req)
			return
		}
		if p.handleChatFailure(lc, path, cand, class, m, st, sk) {
			return
		}
	}
}

// rejectBeforeOutbound 出站前拒绝：翻译路径不可用的粘性/失格请求在此终结。
func (p *Proxy) rejectBeforeOutbound(tw *trackWriter, r *http.Request, nat *inboundNative,
	path attemptPath, sk *stickState) bool {
	// 翻译路径维持无状态拒绝：粘性请求落到翻译候选含治理与插件回落时，服务端状态无处安放
	if nat.sticky() && !path.native {
		p.writeError(tw, r, protocol.TypeBadRequest, protocol.CodePreviousResponseIDUnsupported,
			responsesRejectMessage(protocol.CodePreviousResponseIDUnsupported))
		return true
	}
	// 惰性拒绝升格：该尝试需要翻译出站而请求不可翻译，出站前升格既有 400 信封
	if nat == nil || nat.ineligCode == "" || path.native {
		return false
	}
	// 升格拒绝未由 stick 成功服务,计数三分不缺口(评审 R3)
	if sk.pending {
		cacheAffinityRequests.Inc("stale")
	}
	p.upgradeIneligible(tw, r, nat)
	return true
}

// pickChatKey 取候选 key；池内无可用 key 时登记跳过并告警，返回 nil 交循环继续。
func (p *Proxy) pickChatKey(r *http.Request, topo *topologySnapshot, cand failover.Candidate,
	sk *stickState, st *chatLoopState) *keypool.Key {
	hint := ""
	if cand.Provider == sk.provider {
		hint = sk.keyID // 亲和提示:池内同键直取(健康再判在池内,冷却让位语义不变)
	}
	pool := topo.pools[cand.Provider]
	key := pool.PickWithHint(hint, time.Now())
	if key != nil {
		return key
	}
	// 该 provider 全部 key 冷却或禁用：跳过候选，不计错误类
	st.otherSkipped = true
	p.alerts.Emit("keypool_exhausted:"+cand.Provider, "warning",
		"provider "+cand.Provider+" key 池不可用(全部冷却/禁用)")
	p.logger.Warn("provider key 池不可用,跳过候选",
		"request_id", loggingRequestID(r.Context()), "provider", cand.Provider)
	return nil
}

// chatCandidates 路由候选转评估表候选：同 priority 加权随机起点，priority 递增降级。
func chatCandidates(cands []config.Candidate) []failover.Candidate {
	fcs := make([]failover.Candidate, len(cands))
	for i, c := range cands {
		fcs[i] = failover.Candidate{Provider: c.Provider, Model: c.Model, Priority: c.Priority, Weight: c.Weight}
	}
	return fcs
}

// applyChatPreHooks 入站前置钩子：有状态粘性路由收缩候选域，或执行插件改写并重路由。
// 直通路径一律旁路插件改写：状态请求上下文在服务端指纹无意义，原始体不得被改写过期。
func (p *Proxy) applyChatPreHooks(tw *trackWriter, r *http.Request, body []byte, topo *topologySnapshot,
	nat *inboundNative, req *protocol.ChatRequest,
	cands []config.Candidate) ([]config.Candidate, *protocol.ChatRequest, bool, bool) {
	if nat.sticky() {
		out, sre := p.stickyRoute(r, nat, topo, cands)
		if sre != nil {
			p.writeError(tw, r, protocol.TypeBadRequest, sre.code, sre.msg)
			return nil, req, false, true
		}
		return out, req, false, false
	}
	if nat != nil && nat.ineligCode != "" {
		return cands, req, false, false
	}
	// 插件请求体钩子单次执行，先于语义缓存检索：脱敏必须发生在缓存键计算之前，
	// 敏感载荷不得进语义缓存；改写未发生不做重解析，零开销路径
	if p.plugins == nil {
		return cands, req, false, false
	}
	res, done := p.applyPlugins(tw, r, body)
	if done {
		return nil, req, false, true
	}
	if len(res.ModifiedBy) == 0 {
		return cands, req, false, false
	}
	ncands, nreq, ok := p.rerouteAfterPlugin(tw, r, topo, res)
	if !ok {
		return nil, req, false, true
	}
	p.markPluginModified(tw, r, res.ModifiedBy)
	return ncands, nreq, true, false // 直通回落信号：canonical 已被改写，原始体过期
}

// rerouteAfterPlugin 插件改写产物重解析与重路由，含最终别名的白名单复核。
func (p *Proxy) rerouteAfterPlugin(tw *trackWriter, r *http.Request, topo *topologySnapshot,
	res plugin.ApplyResult) ([]config.Candidate, *protocol.ChatRequest, bool) {
	nreq, err := protocol.ParseChatRequest(res.Body)
	if err != nil {
		p.writeError(tw, r, protocol.TypeBadRequest, "plugin_rejected", "插件改写后的请求体不是合法请求")
		return nil, nil, false
	}
	ncands, ok := topo.routes[nreq.Model]
	if !ok || len(ncands) == 0 {
		p.writeError(tw, r, protocol.TypeBadRequest, "model_not_found", "模型别名不存在: "+nreq.Model)
		return nil, nil, false
	}
	// key 级模型白名单闸之二：插件改写 model 后以最终出站别名为准再校验
	if !p.enforceModelAllowlist(tw, r, nreq.Model) {
		return nil, nil, false
	}
	return ncands, nreq, true
}

// chatCacheLookup 语义缓存命中即回放并终结；未命中返回入库阶段，直通旁路情况为 nil。
// 直通路径：失格请求无指纹，状态请求上下文在服务端，均旁路检索与入库；
// 不可回放条目如 canonical 缺失且协议或表单不符按未命中继续，命中是优化，不得阻断请求。
func (p *Proxy) chatCacheLookup(tw *trackWriter, r *http.Request, nat *inboundNative,
	req *protocol.ChatRequest) (*cachePhase, bool) {
	if nat != nil && (nat.ineligCode != "" || nat.sticky()) {
		return nil, false
	}
	hitStart := time.Now()
	cres, phase := p.lookupCache(r, req)
	if cres != nil && cacheReplayable(cres, nat, req.Stream) {
		p.recordCacheHit(r, req, cres, hitStart)
		p.replayHit(tw, r, cres, nat, req.Stream)
		return nil, true
	}
	if cres == nil {
		return phase, false
	}
	return nil, false
}

// stickState 会话缓存亲和的一次性解析结果。
// pending 表示 lookup 命中尚未终判，请求终结时按「stick 成功服务=hit,其余出口=stale」
// 落定，保证三分计数每请求恰一者。亲和永不新增候选/重试——熔断收缩与池冷却在循环内
// 既有跳过语义恒优先于提示。
type stickState struct {
	id       string
	provider string
	keyID    string
	hit      bool
	found    bool // 存在亲和条目(命中或过期仍保留)
	pending  bool
}

// resolveStick 解析会话缓存亲和(ADR-0020):仅 chat/messages 两条入站 lane 参与(nat 为空
// 且无 lane 标记 =chat 原生;InboundMessages=翻译后);gemini/responses 经 proto 判定排除,
// 不得被误当 chat 参与亲和。
func (p *Proxy) resolveStick(r *http.Request, nat *inboundNative, req *protocol.ChatRequest) stickState {
	var sk stickState
	affinityLane := (nat == nil && laneFrom(r) == "") || (nat != nil && nat.proto == protocol.InboundMessages)
	if !affinityLane {
		return sk
	}
	sk.id = sessionStickID(r, apiKeyIDOf(r), req)
	if sk.id == "" {
		cacheAffinityRequests.Inc("miss")
		return sk
	}
	sp, skey, status := p.cacheAffinity.lookup(sk.id, apiKeyIDOf(r), time.Now())
	switch status {
	case "hit":
		sk.provider, sk.keyID, sk.hit, sk.found, sk.pending = sp, skey, true, true, true
	case "stale":
		cacheAffinityRequests.Inc("stale")
	default:
		cacheAffinityRequests.Inc("miss")
	}
	return sk
}

// skipChatCandidate 候选级跳过判定，先于路径判定与取 key，不计错误类。
func (p *Proxy) skipChatCandidate(r *http.Request, topo *topologySnapshot, nat *inboundNative,
	cand failover.Candidate, midLoop bool) (skipKind, bool) {
	// 健康熔断收缩:降级渠道整路跳过;半开相位 Contracted 为 false,放行流量作试探
	if p.health != nil && p.health.Contracted(cand.Provider) {
		p.logger.Warn("provider 健康熔断中,跳过候选",
			"request_id", loggingRequestID(r.Context()), "provider", cand.Provider)
		return skipCircuit, true
	}
	// oauth lane 收窄(oauth-subscription-lane 契约):仅 chat/messages 入站可达 oauth 渠道,
	// responses/gemini 入站的 oauth 候选跳过(上游零调用,不计错误类)。nat 为空=chat 入站,恒放行。
	if oauthSkippedLane(r, nat, topo.authModes, cand.Provider) {
		p.logger.Warn("oauth 渠道不支持当前入站 lane,跳过候选",
			"request_id", loggingRequestID(r.Context()), "provider", cand.Provider)
		return skipOAuth, true
	}
	// 适配性豁免先于取 key：仅作用于翻译路径，直通候选正常服务工具多轮不豁免；
	// 不计 EMA 与冷却，多候选别名换道继续
	if !pathIsNative(nat, topo, cand) && bridgeExempted(topo.adapters[cand.Provider], midLoop) {
		p.logger.Warn("桥接候选对工具多轮流量适配性豁免,跳过候选",
			"request_id", loggingRequestID(r.Context()), "provider", cand.Provider)
		return skipBridge, true
	}
	return 0, false
}

// pathIsNative 判定候选是否走直通路径；跳过判定先于插件改写，故按未改写形态评估。
func pathIsNative(nat *inboundNative, topo *topologySnapshot, cand failover.Candidate) bool {
	return decideNativePath(nat, topo.adapters, topo.nativeResps, cand, false).native
}

// skipKind 跳过原因，耗尽分支据此在「唯一跳过原因」时给出具体信封。
type skipKind int

const (
	skipCircuit skipKind = iota // 健康熔断收缩
	skipOAuth                   // oauth lane 收窄
	skipBridge                  // 桥接适配性豁免
)

// noteSkip 登记跳过因由。
func (st *chatLoopState) noteSkip(k skipKind) {
	switch k {
	case skipCircuit:
		st.circuitSkipped = true
	case skipOAuth:
		st.oauthSkipped = true
	case skipBridge:
		st.bridgeSkipped = true
	}
}

// chatLoopState 候选循环的跨轮状态。
type chatLoopState struct {
	last           failover.ErrorClass
	bridgeSkipped  bool // 桥接适配性豁免跳过
	otherSkipped   bool // 非桥接原因的候选跳过，如 key 池全冷却
	circuitSkipped bool // 健康熔断收缩的候选跳过;唯一跳过原因时快速失败
	oauthSkipped   bool // oauth lane 收窄跳过;唯一跳过原因时显式信封
}

// finishChatExhausted 候选耗尽出口：单因若为唯一跳过原因则给具体提示，否则统一 all_candidates_failed。
// 能走到耗尽说明每个候选都未对客户端产出，即 decide 为 next_candidate，信封可安全写出。
func (p *Proxy) finishChatExhausted(tw *trackWriter, r *http.Request, req *protocol.ChatRequest,
	nat *inboundNative, st *chatLoopState, sk *stickState) {
	if tw.committed {
		return
	}
	noteStaleIfPending(sk) // hit 已判定但请求未由 stick 成功服务
	unique := st.last == "" && !st.otherSkipped
	// 仅当桥接豁免是唯一的跳过原因时，返回具体提示。
	if st.bridgeSkipped && unique {
		p.writeError(tw, r, protocol.TypeBadRequest, bridgeToolUnsupportedCode,
			"Anthropic 桥接暂不支持工具多轮对话(role=tool / tool_calls),请改用原生兼容上游或去除工具上下文")
		return
	}
	// 仅当健康收缩是唯一跳过原因时快速失败:零上游调用,不空走全超时链
	if st.circuitSkipped && unique {
		p.writeError(tw, r, protocol.TypeUnavailable, providerCircuitOpenCode,
			"上游渠道处于健康熔断期,请稍后重试;可为该别名配置多候选实现透明降级")
		return
	}
	// 仅当 oauth 收窄是唯一跳过原因时显式信封:零上游调用,不伪装成供应商不可用
	if st.oauthSkipped && unique {
		p.writeError(tw, r, protocol.TypeBadRequest, oauthLaneUnsupportedCode,
			"该别名的候选均为 OAuth 订阅渠道,不支持当前入站 lane(仅 chat/messages 可达),请为别名配置非 oauth 候选")
		return
	}
	p.alerts.Emit("all_candidates_failed", "critical",
		"别名 "+req.Model+" 全部候选失败(最后错误: "+string(st.last)+")")
	p.writeError(tw, r, classToType(st.last), "all_candidates_failed",
		"上游供应商暂时不可用,已尝试全部候选(最后错误: "+string(st.last)+")")
}

// finishChatSuccess 成功出口：健康回填、直通亲和回写、会话缓存亲和写侧与缓存入库。
func (p *Proxy) finishChatSuccess(r *http.Request, nat *inboundNative, cand failover.Candidate,
	key *keypool.Key, m *meterState, sk *stickState, cph *cachePhase, req *protocol.ChatRequest) {
	if p.health != nil {
		p.health.ProviderTraffic(cand.Provider, false) // 被动失败窗口
		p.health.TrialResult(cand.Provider, true)      // 半开试探成功即恢复
	}
	p.putNativeAffinity(r, nat, cand, m)
	// 会话缓存亲和写侧:缓存读>0(worth 闸门)才建/续粘。无条目 → 首建粘当次服务者;
	// stick 命中 → 续期;stick 让位转原序服务 → **不改写条目**(保留原 stick,恢复即复粘)。
	if sk.id != "" && m.cacheReadSum() > 0 && (!sk.found || cand.Provider == sk.provider) {
		p.cacheAffinity.note(sk.id, cand.Provider, key.ID, apiKeyIDOf(r))
	}
	if sk.hit && cand.Provider == sk.provider {
		cacheAffinityRequests.Inc("hit")
	} else if sk.hit {
		cacheAffinityRequests.Inc("stale") // stick 不可用转原序服务
	}
	p.storePhase(r, cph, req, cand, m, nat)
}

// handleChatFailure 失败出口分派：返回 true 表示请求已终结，false 表示换道继续。
func (p *Proxy) handleChatFailure(lc *chatLoopCtrl, path attemptPath, cand failover.Candidate,
	class failover.ErrorClass, m *meterState,
	st *chatLoopState, sk *stickState) bool {
	// attempt 已对客户端产出即早退信封或断言出口：请求终结，绝不换道重写
	if lc.tw.committed {
		p.noteCommittedFailure(cand, sk)
		return true
	}
	switch failover.Decide(class, m.firstSet, lc.req.Stream) {
	case failover.ActionNextCandidate:
		st.nextCandidate(p, lc.req, cand, class, sk)
		return false
	case failover.ActionReturn:
		p.returnChat(lc.tw, lc.r, path, cand, class, m, sk)
	default:
		// 静默退出(客户端断连/预算中断)属网关侧策略：premise 未破不清条;
		// 断言出口已写出不再有输出。亲和计数落 stale(请求未由 stick 成功服务)。
		noteStaleIfPending(sk)
	}
	return true
}

// putNativeAffinity 直通成功：responses 入站回写亲和，供粘性路由使用，TTL+LRU 见 affinity.go。
func (p *Proxy) putNativeAffinity(r *http.Request, nat *inboundNative, cand failover.Candidate, m *meterState) {
	if nat != nil && nat.proto == protocol.InboundResponses && m.nativeRespID != "" {
		p.affinity.Put(m.nativeRespID, cand.Provider, apiKeyIDOf(r))
	}
}

// noteStaleIfPending 请求未由 stick 成功服务即落 stale，保证三分计数每请求恰一者。
func noteStaleIfPending(sk *stickState) {
	if sk.pending {
		cacheAffinityRequests.Inc("stale")
	}
}

// noteCommittedFailure 已提交出口：stick 键上的真实失败清条，亲和计数落 stale。
func (p *Proxy) noteCommittedFailure(cand failover.Candidate, sk *stickState) {
	p.dropStickOnFailure(cand, sk)
	noteStaleIfPending(sk)
}

// dropStickOnFailure stick 键上的真实失败即缓存前提已破，立即清条(spec「失败即清」)。
func (p *Proxy) dropStickOnFailure(cand failover.Candidate, sk *stickState) {
	if sk.hit && cand.Provider == sk.provider {
		p.cacheAffinity.drop(sk.id)
	}
}

// nextCandidate 换道出口：记录最后错误，实证 stick 键失败，回填健康换道统计。
func (st *chatLoopState) nextCandidate(p *Proxy, req *protocol.ChatRequest, cand failover.Candidate,
	class failover.ErrorClass, sk *stickState) {
	st.last = class
	p.dropStickOnFailure(cand, sk) // 换道即清：stick 键上的失败已实证(评审 C1 契约接线)
	if p.health != nil {
		p.health.AliasSwitch(req.Model)               // 换道率分子
		p.health.ProviderTraffic(cand.Provider, true) // 被动失败窗口
		p.health.TrialResult(cand.Provider, false)    // 半开试探失败回降
	}
}

// returnChat 不换道终结出口：直通错误原样透传，翻译路径归一信封携带上游可读 message。
func (p *Proxy) returnChat(tw *trackWriter, r *http.Request, path attemptPath, cand failover.Candidate,
	class failover.ErrorClass, m *meterState, sk *stickState) {
	p.dropStickOnFailure(cand, sk) // 不换道类的上游失败同样实证前提已破
	noteStaleIfPending(sk)
	if path.native && len(m.upstreamBody) > 0 {
		// 直通错误透传：不换道类的上游错误信封原样到达客户端，零归一改写
		p.writeUpstreamPassthrough(tw, m)
		return
	}
	// 不换道直接返回，参数问题换候选也会拒：归一信封携带上游可读 message
	p.writeError(tw, r, classToType(class), string(class), m.upstreamMsg)
}

// govern 按目标候选解析能力约束并执行治理，目录未声明时原件零干预返回。
// s 由调用方传入，与适配器取自同一快照，治理与出站构造不跨快照错位。
func (p *Proxy) govern(s *topologySnapshot, req *protocol.ChatRequest,
	cand failover.Candidate) (*protocol.ChatRequest, []governChange, string) {
	capability, ok := s.caps[governKey{cand.Provider, cand.Model}]
	if !ok {
		return req, nil, ""
	}
	return governChat(req, capability, p.strictGov)
}

// traceGovern 留痕双通道：结构化日志逐条明细，客户端可见响应头参数清单。
// 头在响应提交前写入，此处位于任何 WriteHeader 之前；换道后无干预即清除上一目标残留。
func (p *Proxy) traceGovern(w http.ResponseWriter, r *http.Request, cand failover.Candidate, changes []governChange) {
	if len(changes) == 0 {
		w.Header().Del("X-Muniu-Normalized")
		return
	}
	params := make([]string, 0, len(changes))
	for _, ch := range changes {
		params = append(params, ch.Param)
		p.logger.Info("请求参数治理干预",
			"request_id", loggingRequestID(r.Context()),
			"provider", cand.Provider, "model", cand.Model,
			"param", ch.Param, "action", ch.Action, "from", ch.From, "to", ch.To,
		)
	}
	w.Header().Set("X-Muniu-Normalized", strings.Join(params, ","))
}

// attempt 单候选执行：按直通或翻译路径与流式与否分发，退出时统一落账，
// 成功、失败、断连三态都记，defer 保证任何出口都落账。
// snap 由调用方传入，即请求入口加载的同一快照，路径判定与执行取自同一视图，
// 在途请求持入口快照完成，attempt 内不得二次 Load 混用新旧。
func (p *Proxy) attempt(w http.ResponseWriter, r *http.Request, snap *topologySnapshot,
	req *protocol.ChatRequest, nat *inboundNative, path attemptPath, cand failover.Candidate,
	key *keypool.Key, m *meterState) (handled bool, class failover.ErrorClass) {
	ad, ok := snap.adapters[cand.Provider]
	if !ok {
		// 拓扑快照保证 provider 已装配，防御性兜底
		p.logger.Error("provider 未注册", "provider", cand.Provider)
		p.writeError(w, r, protocol.TypeInternal, "adapter_missing", "内部配置错误: 适配器未注册")
		return true, failover.ClassUnknown
	}
	// 出站 client 按渠道解析：声明代理渠道走独立 client（对 env 免疫），其余默认（见 clientFor）
	upstream := p.clientFor(snap, cand.Provider)
	p.fillBudgetEstimate(r, req, nat, m)

	prep := p.prepareAttempt(w, r, snap, req, nat, &path, cand, m)
	if prep.done() {
		return true, prep.class
	}
	p.traceGovern(w, r, cand, prep.changes)
	setWriterNativeMode(w, path.native) // writer 模式位：首字节提交前唯一切换点

	if path.native {
		return p.execNativeAttempt(w, r, upstream, ad, path, key, m, nat, cand)
	}
	return p.execTranslateAttempt(w, r, upstream, ad, req, prep.req, key, m, cand)
}

// attemptPrep 治理阶段结论：done 表示已在治理阶段终结，req 为翻译路径消费的 canonical。
type attemptPrep struct {
	req     *protocol.ChatRequest
	changes []governChange
	class   failover.ErrorClass // 非空即治理阶段终结
}

// done 治理阶段是否已对客户端写出终结信封。
func (a attemptPrep) done() bool { return a.class != "" }

// fillBudgetEstimate 请求级成本预算装填：每 attempt 从 canonical 估算输入，价目按别名解析且
// 跨候选同价；失格请求 canonical 不可得估算为 0，usage 真值路径仍生效。
func (p *Proxy) fillBudgetEstimate(r *http.Request, req *protocol.ChatRequest, nat *inboundNative, m *meterState) {
	info := auth.KeyFrom(r.Context())
	if info == nil || info.MaxRequestCostCredits <= 0 {
		return
	}
	m.budget = info.MaxRequestCostCredits
	m.budgetModel = req.Model
	if nat == nil || nat.ineligCode == "" {
		inputText, _ := json.Marshal(req.Messages)
		m.budgetEstIn = estimateInputTokens(string(inputText), req.Model)
	}
}

// governNativeAttempt 直通 attempt 的治理评估：gemini 的采样参数在 generationConfig 嵌套且命名
// 不同(maxOutputTokens/topK)，原始体评估会让能力约束失效，故取 canonical 与翻译路径同一判据；
// 其余协议字段名与 canonical 同域，维持原始体评估以覆盖翻译器丢弃的参数。干预即回落翻译路径。
func (p *Proxy) governNativeAttempt(r *http.Request, snap *topologySnapshot, req *protocol.ChatRequest,
	nat *inboundNative, path *attemptPath, cand failover.Candidate) ([]governChange, string) {
	capability, ok := snap.caps[governKey{cand.Provider, cand.Model}]
	if !ok {
		return nil, ""
	}
	evalReq := &protocol.ChatRequest{Raw: nat.raw}
	if nat.proto == protocol.InboundGemini && req != nil {
		evalReq = req
	}
	_, changes, reject := governChat(evalReq, capability, p.strictGov)
	if len(changes) > 0 {
		path.native = false
		path.fallback = "governance_intervened"
		p.logger.Info("直通回落翻译路径(治理干预)",
			"request_id", loggingRequestID(r.Context()),
			"provider", cand.Provider, "model", cand.Model, "changes", len(changes))
	}
	return changes, reject
}

// prepareAttempt 请求参数治理：每次 attempt 从原始请求出发按目标 (provider, model) 重新治理，
// 换道后不得沿用上一目标的收敛结果，strict 命中即 400 终结。
// 直通 attempt 默认以原始体评估治理，翻译器静默丢弃的参数不在 canonical，只评 canonical 会漏；
// gemini 例外取 canonical（采样参数在 generationConfig 嵌套且命名不同，raw 评估会失效，见
// governNativeAttempt）。失格请求 raw 同样可评，声明约束经干预回落翻译，由升格拒绝兜底，语义一致。
// 翻译路径治理消费 canonical：失格请求 canonical 不可得，治理不可评估，直通免治。
// 治理干预可把直通降级为翻译路径，故 path 经指针就地改写。
func (p *Proxy) prepareAttempt(w http.ResponseWriter, r *http.Request, snap *topologySnapshot,
	req *protocol.ChatRequest, nat *inboundNative, path *attemptPath, cand failover.Candidate,
	m *meterState) attemptPrep {
	var out attemptPrep
	var greject string
	if path.native {
		out.changes, greject = p.governNativeAttempt(r, snap, req, nat, path, cand)
	}
	if nat != nil && nat.ineligCode != "" {
		// 失格请求单一出口：治理干预落翻译路径或 strict 命中即升格既有 400 信封，
		// canonical 不可得翻译本就不可服务，param 冲突相对失格是次要原因
		if !path.native || greject != "" {
			p.upgradeIneligible(w, r, nat)
			out.class = failover.ClassBadRequest
		}
		return out
	}
	if !path.native {
		out.req, out.changes, greject = p.govern(snap, req, cand)
	}
	if greject != "" {
		p.writeError(w, r, protocol.TypeBadRequest, "param_rejected", greject)
		out.class = failover.ClassBadRequest
		return out
	}
	return out
}

// execNativeAttempt 直通路径出站构造与分发：原始体加鉴权注入，流式与否各走专路径。
func (p *Proxy) execNativeAttempt(w http.ResponseWriter, r *http.Request, upstream *http.Client,
	ad adapter.Adapter, path attemptPath, key *keypool.Key, m *meterState, nat *inboundNative,
	cand failover.Candidate) (bool, failover.ErrorClass) {
	out, err := path.preparer.PrepareNative(&protocol.NativeRequest{
		Proto: nat.proto, Raw: nat.raw, Stream: nat.stream,
	}, cand.Model, key.Value)
	if err != nil {
		p.writeError(w, r, protocol.TypeBadRequest, "bad_request", "出站请求构造失败")
		return true, failover.ClassBadRequest
	}
	if nat.stream {
		return p.attemptNativeStream(w, r, upstream, ad, path.preparer, out, m, nat, cand)
	}
	return p.attemptNativeNonStream(w, r, upstream, ad, path.preparer, out, m, nat, cand)
}

// execTranslateAttempt 翻译路径出站构造与分发：计量落账 defer 覆盖任何出口。
func (p *Proxy) execTranslateAttempt(w http.ResponseWriter, r *http.Request, upstream *http.Client,
	ad adapter.Adapter, req, greq *protocol.ChatRequest, key *keypool.Key, m *meterState,
	cand failover.Candidate) (bool, failover.ErrorClass) {
	out, err := ad.Prepare(greq, cand.Model, key.Value)
	if err != nil {
		p.writeError(w, r, protocol.TypeBadRequest, "bad_request", "出站请求构造失败")
		return true, failover.ClassBadRequest
	}

	// 计量落账：任何出口统一执行
	defer func() {
		inputText, _ := json.Marshal(req.Messages)
		p.record(r, req.Model, cand.Model, cand, string(inputText), m)
	}()

	if req.Stream {
		handled, class := p.attemptStream(w, r, upstream, ad, out, m)
		if class == "" && m.outBody == nil {
			// 流式成功：按累计内容拼装标准响应体供语义缓存入库，非 stop 终态返回 nil 不入库
			m.outBody = m.assembleStreamBody(cand.Model)
		}
		return handled, class
	}
	return p.attemptNonStream(w, r, upstream, ad, out, m)
}

// outboundRequest 组装上游请求，是 context 链断连传导的必经环节。
func (p *Proxy) outboundRequest(r *http.Request, opCtx context.Context, out *adapter.Outbound) (*http.Request, error) {
	req, err := http.NewRequestWithContext(opCtx, http.MethodPost, out.URL, //nolint:gosec // URL 由适配器按渠道 base_url 构造
		strings.NewReader(string(out.Body)))
	if err != nil {
		return nil, err
	}
	for k, vs := range out.Header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	return req, nil
}

// writeError 按统一错误信封和状态映射写出代理错误。
func (p *Proxy) writeError(w http.ResponseWriter, r *http.Request, typ, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(protocol.StatusForType(typ))
	_ = json.NewEncoder(w).Encode(protocol.NewErrorEnvelope(typ, code, msg, logging.RequestIDFrom(r.Context())))
}

// classToType 判定表 ErrorClass 映射为错误类型，状态映射前一步。
func classToType(c failover.ErrorClass) string {
	switch c {
	case failover.ClassBadRequest:
		return protocol.TypeBadRequest
	case failover.ClassAuthFailed:
		return protocol.TypeAuthFailed
	case failover.ClassRateLimited:
		return protocol.TypeRateLimited
	case failover.ClassTimeoutFirst:
		return protocol.TypeTimeout
	case failover.ClassContextCanceled:
		return protocol.TypeInternal
	default: // upstream_5xx / dial_fail / mid_stream_fail / unknown
		return protocol.TypeUpstreamError
	}
}

// upstreamMessage 尽力抽取上游错误 body 中人类可读 message，供终结错误透传；失败给缺省。
func upstreamMessage(body []byte, fallback string) string {
	var parsed struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &parsed); err == nil && parsed.Error.Message != "" {
		return parsed.Error.Message
	}
	return fallback
}

// marshalEnvelope / unmarshalEnvelope 测试与断言出口共用。
func marshalEnvelope(env *protocol.ErrorEnvelope) ([]byte, error) { return json.Marshal(env) }

func unmarshalEnvelope(body string, env *protocol.ErrorEnvelope) error {
	return json.Unmarshal([]byte(body), env)
}

// loggingRequestID 从 ctx 取 request_id 的包内别名，日志字段统一口径。
func loggingRequestID(ctx context.Context) string {
	return logging.RequestIDFrom(ctx)
}
