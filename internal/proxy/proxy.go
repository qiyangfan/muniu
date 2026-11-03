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

// Config 代理装配参数，由 New 消费，结构化形态避免参数膨胀。
type Config struct {
	Registry         *adapter.Registry
	Providers        []config.Provider // provider 定义，拓扑快照的池保留基准
	Routes           map[string][]config.Candidate
	Settlement       string // 网关本位币；空为 CNY，无价目行也须同币种。
	ImagePricing     *metering.ImagePriceDirectory
	Pricing          *metering.PricingDirectory // 价目目录，动态改价由管理面原子替换，转发只读；nil 表示成本记 0
	Timeouts         Timeouts
	Pipeline         *metering.Pipeline       // 计量管道；nil 表示不计量，纯转发场景
	Pools            map[string]*keypool.Pool // provider 到 key 池；缺池则该 provider 无法出站
	TPM              ratelimit.TPMRecorder    // TPM 记账回填，TPM 或 redis 后端，前置判定在 Gate 中间件
	Cache            *semantic.Cache          // 语义缓存；nil 表示关闭
	TenantSwitch     func(string) bool        // 租户语义缓存开关，装配层提供 DB 查询闭包
	Capabilities     []config.ModelCapability // 模型能力目录；空表示全部零干预透传
	StrictGovernance bool                     // true 表示命中能力约束即 400，缺省宽松放行并留痕
	// InheritProxy 控制面拓扑来源标记:控制面载荷不含 proxy_url 字段,置 true 时入站声明为空
	// 的渠道沿用旧快照代理声明(防静默摘除);配置文件源(SIGHUP/启动)必须保持 false——
	// 用户从 yaml 删除 proxy_url 是显式清空,沿用会吞掉变更(评审 R1)。
	InheritProxy bool
	Plugins      *plugin.Registry   // WASM 插件注册表；nil 表示插件禁用，主路径零开销
	Alerts       *alerting.Notifier // 告警通知器；nil 表示关闭，Emit 零开销
	Health       *health.Manager    // 渠道健康熔断管理器；nil = 关闭,候选域零收缩
	RandIntn     func(int) int      // 候选起点随机源；nil 用全局随机，测试可注入
	Logger       *slog.Logger
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
	tenantSw      func(string) bool
	strictGov     bool
	plugins       *plugin.Registry
	alerts        *alerting.Notifier
	health        *health.Manager
	randIntn      func(int) int
	logger        *slog.Logger
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
		randIntn = func(n int) int { return rand.IntN(n) }
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
			logger.Warn("渠道出站代理非法,降级默认出站", "provider", prov.Name, "proxy_url", redactProxyURL(prov.ProxyURL), "error", err.Error())
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
func (p *Proxy) cacheTenantEnabled(tenantID string) bool {
	if p.tenantSw == nil {
		return false
	}
	return p.tenantSw(tenantID)
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
	var req *protocol.ChatRequest
	if nat != nil && nat.ineligCode != "" {
		// 翻译失格：canonical 不可得，最小路由载体，治理与缓存不可评估，直通专属语义
		req = &protocol.ChatRequest{Model: nat.model, Stream: nat.stream}
	} else {
		req, err = protocol.ParseChatRequest(body)
		if err != nil {
			p.writeError(tw, r, protocol.TypeBadRequest, "bad_request", "请求不符合 OpenAI Chat Completions 格式")
			return
		}
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

	// 有状态粘性路由只走原生 responses 路径：候选域收缩至亲和 provider 或自解释错误。
	// 状态请求旁路插件改写与语义缓存：上下文在服务端指纹无意义，原始体不得被改写过期。
	pluginModified := false
	if nat.sticky() {
		var sre *stickyError
		cands, sre = p.stickyRoute(r, nat, topo, cands)
		if sre != nil {
			p.writeError(tw, r, protocol.TypeBadRequest, sre.code, sre.msg)
			return
		}
	} else if nat == nil || nat.ineligCode == "" {
		// 插件请求体钩子单次执行，先于语义缓存检索：脱敏必须发生在缓存键计算之前，
		// 敏感载荷不得进语义缓存；改写未发生不做重解析，零开销路径
		if p.plugins != nil {
			res, done := p.applyPlugins(tw, r, body)
			if done {
				return
			}
			if len(res.ModifiedBy) > 0 {
				nreq, err := protocol.ParseChatRequest(res.Body)
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
				pluginModified = true // 直通回落信号：canonical 已被改写，原始体过期
			}
		}
	}

	// 语义缓存命中时直接回放；未命中则在成功响应后尝试写入。
	// 直通路径：失格请求无指纹，状态请求上下文在服务端，均旁路检索与入库；
	// 不可回放条目如 canonical 缺失且协议或表单不符按未命中继续，命中是优化，不得阻断请求。
	var cph *cachePhase
	if nat == nil || (nat.ineligCode == "" && !nat.sticky()) {
		hitStart := time.Now()
		if cres, phase := p.lookupCache(r, req); cres != nil && cacheReplayable(cres, nat, req.Stream) {
			p.recordCacheHit(r, req, cres, hitStart)
			p.replayHit(tw, r, cres, nat, req.Stream)
			return
		} else if cres == nil {
			cph = phase
		}
	}

	// 价目在请求入口选定；预检、候选尝试与落账均使用这份不可变快照。
	var requestPrices *metering.Pricing
	if pt := p.pricing.Get(req.Model); pt != nil {
		pr := pt.At(time.Now())
		requestPrices = &pr
	}
	var requestBudget int64
	if info := auth.KeyFrom(r.Context()); info != nil {
		requestBudget = info.MaxRequestCostCredits
	}
	if requestBudget > 0 && (nat == nil || nat.ineligCode == "") {
		if msg, over := preflightBudgetExceeded(req, requestBudget, requestPrices); over {
			p.writeError(tw, r, protocol.TypeQuotaExceeded, costBudgetExceededCode, msg)
			return
		}
	}

	// 构造候选迭代器：同 priority 加权随机起点，priority 递增降级
	fcs := make([]failover.Candidate, len(cands))
	for i, c := range cands {
		fcs[i] = failover.Candidate{Provider: c.Provider, Model: c.Model, Priority: c.Priority, Weight: c.Weight}
	}
	// 会话缓存亲和(ADR-0020):仅 chat/messages 两条入站 lane 参与(nat 为空=chat 原生;
	// InboundMessages=翻译后)——responses/gemini 登记为零触碰,亲和表零读写、计数零介入。
	// 身份解析一次;stick 健康时 iterator 组内起点提示+池内同键提示。亲和永不新增候选/重试
	// ——熔断收缩与池冷却在循环内既有跳过语义恒优先于提示。pendingHit:lookup 命中尚未终判,
	// 请求终结时按「stick 成功服务=hit,其余出口=stale」落定,保证三分计数每请求恰一者。
	affinityLane := nat == nil || nat.proto == protocol.InboundMessages
	stickID, stickProvider, stickKeyID := "", "", ""
	stickHit, entryFound, pendingHit := false, false, false
	if affinityLane {
		stickID = sessionStickID(r, apiKeyIDOf(r), req)
		if stickID != "" {
			sp, sk, status := p.cacheAffinity.lookup(stickID, apiKeyIDOf(r), time.Now())
			switch status {
			case "hit":
				stickProvider, stickKeyID, stickHit, entryFound, pendingHit = sp, sk, true, true, true
			case "stale":
				cacheAffinityRequests.Inc("stale")
			default:
				cacheAffinityRequests.Inc("miss")
			}
		} else {
			cacheAffinityRequests.Inc("miss")
		}
	}
	iter := failover.NewIteratorWithStick(fcs, p.randIntn, stickProvider)
	var last failover.ErrorClass
	// 桥接工具多轮边界：多轮中途流量对翻译路径的桥接候选不可服务，
	// 直通候选正常服务，不受此边界约束
	midLoop := chatToolLoopInflight(req)
	bridgeSkipped := false
	otherSkipped := false   // 非桥接原因的候选跳过，如 key 池全冷却
	circuitSkipped := false // 健康熔断收缩的候选跳过;唯一跳过原因时快速失败
	if p.health != nil {
		p.health.AliasRequest(req.Model) // 换道率分母:每请求一次
	}
	for {
		cand, ok := iter.Next()
		if !ok {
			// 候选耗尽：code=all_candidates_failed，类型沿用最后错误映射，
			// message 如实说明已尝试全部候选。
			// 能走到耗尽说明每个候选都未对客户端产出，即 decide 为 next_candidate，信封可安全写出
			if tw.committed {
				return
			}
			if pendingHit {
				cacheAffinityRequests.Inc("stale") // hit 已判定但请求未由 stick 成功服务
			}
			// 仅当桥接豁免是唯一的跳过原因时，返回具体提示。
			if bridgeSkipped && last == "" && !otherSkipped {
				p.writeError(tw, r, protocol.TypeBadRequest, bridgeToolUnsupportedCode,
					"Anthropic 桥接暂不支持工具多轮对话(role=tool / tool_calls),请改用原生兼容上游或去除工具上下文")
				return
			}
			// 仅当健康收缩是唯一跳过原因时快速失败:零上游调用,不空走全超时链
			if circuitSkipped && last == "" && !otherSkipped {
				p.writeError(tw, r, protocol.TypeUnavailable, providerCircuitOpenCode,
					"上游渠道处于健康熔断期,请稍后重试;可为该别名配置多候选实现透明降级")
				return
			}
			typ := classToType(last)
			p.alerts.Emit("all_candidates_failed", "critical",
				"别名 "+req.Model+" 全部候选失败(最后错误: "+string(last)+")")
			p.writeError(tw, r, typ, "all_candidates_failed",
				"上游供应商暂时不可用,已尝试全部候选(最后错误: "+string(last)+")")
			return
		}
		// 健康熔断收缩:降级渠道整路跳过,先于路径判定与取 key,不计错误类;
		// 半开相位 Contracted 为 false,放行流量作试探(试探结果经 TrialResult 回填)
		if p.health != nil && p.health.Contracted(cand.Provider) {
			circuitSkipped = true
			p.logger.Warn("provider 健康熔断中,跳过候选",
				"request_id", loggingRequestID(r.Context()), "provider", cand.Provider)
			continue
		}
		// 每 attempt 直通或翻译路径判定，决策点唯一，chat 入站恒翻译
		path := decideNativePath(nat, topo.adapters, topo.nativeResps, cand, pluginModified)
		// 适配性豁免先于取 key：仅作用于翻译路径，直通候选正常服务工具多轮不豁免；
		// 不计 EMA 与冷却，多候选别名换道继续
		if !path.native && bridgeExempted(topo.adapters[cand.Provider], midLoop) {
			bridgeSkipped = true
			p.logger.Warn("桥接候选对工具多轮流量适配性豁免,跳过候选",
				"request_id", loggingRequestID(r.Context()), "provider", cand.Provider)
			continue
		}
		// 翻译路径维持无状态拒绝：粘性请求落到翻译候选含治理与插件回落时，服务端状态无处安放
		if nat.sticky() && !path.native {
			p.writeError(tw, r, protocol.TypeBadRequest, protocol.CodePreviousResponseIDUnsupported,
				responsesRejectMessage(protocol.CodePreviousResponseIDUnsupported))
			return
		}
		// 惰性拒绝升格：该尝试需要翻译出站而请求不可翻译，出站前升格既有 400 信封
		if nat != nil && nat.ineligCode != "" && !path.native {
			if pendingHit {
				cacheAffinityRequests.Inc("stale") // 升格拒绝未由 stick 成功服务,计数三分不缺口(评审 R3)
			}
			p.upgradeIneligible(tw, r, nat)
			return
		}

		// 取 key、执行、按 Decide 动作分支、结果回写 key 池
		pool := topo.pools[cand.Provider]
		hint := ""
		if cand.Provider == stickProvider {
			hint = stickKeyID // 亲和提示:池内同键直取(健康再判在池内,冷却让位语义不变)
		}
		key := pool.PickWithHint(hint, time.Now())
		if key == nil {
			// 该 provider 全部 key 冷却或禁用：跳过候选，不计错误类
			otherSkipped = true
			p.alerts.Emit("keypool_exhausted:"+cand.Provider, "warning",
				"provider "+cand.Provider+" key 池不可用(全部冷却/禁用)")
			p.logger.Warn("provider key 池不可用,跳过候选",
				"request_id", loggingRequestID(r.Context()), "provider", cand.Provider)
			continue
		}
		m := &meterState{start: time.Now(), status: "ok", priceSnapshot: requestPrices, priceSnapshotTaken: true, budget: requestBudget, budgetPrices: requestPrices}
		handled, class := p.attempt(tw, r, topo, req, nat, path, cand, key, m)
		if pool != nil {
			p.reportKeyOutcome(pool, key, class, handled, m)
		}
		if class == "" {
			if p.health != nil {
				p.health.ProviderTraffic(cand.Provider, false) // 被动失败窗口
				p.health.TrialResult(cand.Provider, true)      // 半开试探成功即恢复
			}
			// 直通成功：responses 入站回写亲和，供粘性路由使用，TTL+LRU 见 affinity.go
			if nat != nil && nat.proto == protocol.InboundResponses && m.nativeRespID != "" {
				p.affinity.Put(m.nativeRespID, cand.Provider, apiKeyIDOf(r))
			}
			// 会话缓存亲和写侧:缓存读>0(worth 闸门)才建/续粘。无条目 → 首建粘当次服务者;
			// stick 命中 → 续期;stick 让位转原序服务 → **不改写条目**(保留原 stick,恢复即复粘)。
			if stickID != "" && m.cacheReadSum() > 0 && (!entryFound || cand.Provider == stickProvider) {
				p.cacheAffinity.note(stickID, cand.Provider, key.ID, apiKeyIDOf(r))
			}
			if stickHit {
				if cand.Provider == stickProvider {
					cacheAffinityRequests.Inc("hit")
				} else {
					cacheAffinityRequests.Inc("stale") // stick 不可用转原序服务
				}
			}
			p.storePhase(r, cph, req, cand, m, nat)
			return
		}
		// attempt 已对客户端产出即早退信封或断言出口：请求终结，绝不换道重写
		if tw.committed {
			if stickHit && cand.Provider == stickProvider {
				// 断言出口=stick 键上的真实失败：缓存前提已破，立即清条(spec「失败即清」)
				p.cacheAffinity.drop(stickID)
			}
			if pendingHit {
				cacheAffinityRequests.Inc("stale")
			}
			return
		}
		switch failover.Decide(class, m.firstSet, req.Stream) {
		case failover.ActionNextCandidate:
			last = class
			if stickHit && cand.Provider == stickProvider {
				p.cacheAffinity.drop(stickID) // 换道即清：stick 键上的失败已实证(评审 C1 契约接线)
			}
			if p.health != nil {
				p.health.AliasSwitch(req.Model)               // 换道率分子
				p.health.ProviderTraffic(cand.Provider, true) // 被动失败窗口
				p.health.TrialResult(cand.Provider, false)    // 半开试探失败回降
			}
			continue
		case failover.ActionReturn:
			if stickHit && cand.Provider == stickProvider {
				p.cacheAffinity.drop(stickID) // 不换道类的上游失败同样实证前提已破
			}
			if pendingHit {
				cacheAffinityRequests.Inc("stale")
			}
			if path.native && len(m.upstreamBody) > 0 {
				// 直通错误透传：不换道类的上游错误信封原样到达客户端，零归一改写
				p.writeUpstreamPassthrough(tw, m)
				return
			}
			// 不换道直接返回，参数问题换候选也会拒：归一信封携带上游可读 message
			p.writeError(tw, r, classToType(class), string(class), m.upstreamMsg)
			return
		case failover.ActionSilent, failover.ActionAbort:
			// 静默退出(客户端断连/预算中断)属网关侧策略：premise 未破不清条;
			// 断言出口已写出不再有输出。亲和计数落 stale(请求未由 stick 成功服务)。
			if pendingHit {
				cacheAffinityRequests.Inc("stale")
			}
			return
		}
	}
}

// govern 按目标候选解析能力约束并执行治理，目录未声明时原件零干预返回。
// s 由调用方传入，与适配器取自同一快照，治理与出站构造不跨快照错位。
func (p *Proxy) govern(s *topologySnapshot, req *protocol.ChatRequest, cand failover.Candidate) (*protocol.ChatRequest, []governChange, string) {
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
func (p *Proxy) attempt(w http.ResponseWriter, r *http.Request, snap *topologySnapshot, req *protocol.ChatRequest, nat *inboundNative, path attemptPath, cand failover.Candidate, key *keypool.Key, m *meterState) (handled bool, class failover.ErrorClass) {
	ad, ok := snap.adapters[cand.Provider]
	if !ok {
		// 拓扑快照保证 provider 已装配，防御性兜底
		p.logger.Error("provider 未注册", "provider", cand.Provider)
		p.writeError(w, r, protocol.TypeInternal, "adapter_missing", "内部配置错误: 适配器未注册")
		return true, failover.ClassUnknown
	}
	// 出站 client 按渠道解析：声明代理渠道走独立 client（对 env 免疫），其余默认（见 clientFor）
	upstream := p.clientFor(snap, cand.Provider)
	// 请求级成本预算装填：每 attempt 从 canonical 估算输入，价目按别名解析且跨候选同价；
	// 失格请求 canonical 不可得估算为 0，usage 真值路径仍生效
	if info := auth.KeyFrom(r.Context()); info != nil && info.MaxRequestCostCredits > 0 {
		m.budget = info.MaxRequestCostCredits
		m.budgetModel = req.Model

		if nat == nil || nat.ineligCode == "" {
			inputText, _ := json.Marshal(req.Messages)
			m.budgetEstIn = estimateInputTokens(string(inputText), req.Model)
		}
	}
	// 请求参数治理：每次 attempt 从原始请求出发按目标 (provider, model) 重新治理，
	// 换道后不得沿用上一目标的收敛结果，strict 命中即 400 终结。
	// 直通 attempt 以原始体评估治理，翻译器静默丢弃的参数不在 canonical，只评 canonical 会漏；
	// 失格请求 raw 同样可评，声明约束经干预回落翻译，由升格拒绝兜底，语义一致。
	// 翻译路径治理消费 canonical：失格请求 canonical 不可得，治理不可评估，直通免治。
	var greq *protocol.ChatRequest
	var gchanges []governChange
	var greject string
	if path.native {
		if capability, ok := snap.caps[governKey{cand.Provider, cand.Model}]; ok {
			_, gchanges, greject = governChat(&protocol.ChatRequest{Raw: nat.raw}, capability, p.strictGov)
			if len(gchanges) > 0 {
				path.native = false
				path.fallback = "governance_intervened"
				p.logger.Info("直通回落翻译路径(治理干预)",
					"request_id", loggingRequestID(r.Context()),
					"provider", cand.Provider, "model", cand.Model, "changes", len(gchanges))
			}
		}
	}
	if nat != nil && nat.ineligCode != "" {
		// 失格请求单一出口：治理干预落翻译路径或 strict 命中即升格既有 400 信封，
		// canonical 不可得翻译本就不可服务，param 冲突相对失格是次要原因
		if !path.native || greject != "" {
			p.upgradeIneligible(w, r, nat)
			return true, failover.ClassBadRequest
		}
	} else {
		if !path.native {
			greq, gchanges, greject = p.govern(snap, req, cand)
		}
		if greject != "" {
			p.writeError(w, r, protocol.TypeBadRequest, "param_rejected", greject)
			return true, failover.ClassBadRequest
		}
	}
	p.traceGovern(w, r, cand, gchanges)
	setWriterNativeMode(w, path.native) // writer 模式位：首字节提交前唯一切换点

	if path.native {
		out, err := path.preparer.PrepareNative(&protocol.NativeRequest{Proto: nat.proto, Raw: nat.raw}, cand.Model, key.Value)
		if err != nil {
			p.writeError(w, r, protocol.TypeBadRequest, "bad_request", "出站请求构造失败")
			return true, failover.ClassBadRequest
		}
		if nat.stream {
			return p.attemptNativeStream(w, r, upstream, ad, path.preparer, out, m, nat, cand)
		}
		return p.attemptNativeNonStream(w, r, upstream, ad, path.preparer, out, m, nat, cand)
	}

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
	req, err := http.NewRequestWithContext(opCtx, http.MethodPost, out.URL, strings.NewReader(string(out.Body)))
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
