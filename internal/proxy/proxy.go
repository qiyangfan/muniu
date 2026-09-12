// Package proxy 是编排核心(architecture 生命周期 7-12 步)与唯一「认识所有包」的组装层。
// Phase 2 接入计量第 11 步(成功/失败/断连三态都记);候选迭代器接口就位但只走单候选,
// Phase 3 在此填 failover 循环。
package proxy

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/solvela/muniu/internal/adapter"
	"github.com/solvela/muniu/internal/config"
	"github.com/solvela/muniu/internal/failover"
	"github.com/solvela/muniu/internal/keypool"
	"github.com/solvela/muniu/internal/logging"
	"github.com/solvela/muniu/internal/metering"
	"github.com/solvela/muniu/internal/plugin"
	"github.com/solvela/muniu/internal/protocol"
	"github.com/solvela/muniu/internal/ratelimit"
	"github.com/solvela/muniu/internal/semantic"
)

// maxInboundBody 入站请求体上限(32MB,防恶意大包;模型上下文远小于此)。
const maxInboundBody = 32 << 20

// Timeouts 三层超时的运行态形态(语义见 proxy-and-streaming「超时模型」)。
type Timeouts struct {
	Dial       time.Duration
	FirstToken time.Duration
	ChunkIdle  time.Duration
}

// Config 代理装配参数(New 消费;结构化形态避免参数膨胀)。
type Config struct {
	Registry         *adapter.Registry
	Routes           map[string][]config.Candidate
	Pricing          *metering.PricingDirectory // 价目目录(动态改价:管理面原子替换,转发只读);nil = 成本记 0
	Timeouts         Timeouts
	Pipeline         *metering.Pipeline       // 计量管道;nil = 不计量(纯转发场景)
	Pools            map[string]*keypool.Pool // provider → key 池;缺池则该 provider 无法出站
	TPM              *ratelimit.TPM           // TPM 滑动窗口;请求完成后记账(前置判定在 Gate 中间件)
	Cache            *semantic.Cache          // 语义缓存;nil = 关闭
	TenantSwitch     func(string) bool        // 租户语义缓存开关(装配层提供 DB 查询闭包)
	Capabilities     []config.ModelCapability // 模型能力目录(ADR-0010);空 = 全部零干预透传
	StrictGovernance bool                     // true = 命中能力约束即 400(缺省宽松放行+留痕)
	Plugins          *plugin.Registry         // WASM 插件注册表(ADR-0011);nil = 插件禁用,主路径零开销
	RandIntn         func(int) int            // 候选起点随机源;nil 用全局随机(测试可注入)
	Logger           *slog.Logger
}

// Proxy 编排器:注册表 + 路由 + key 池 + 三层超时 + 计量管道。
type Proxy struct {
	registry  *adapter.Registry
	routes    map[string][]config.Candidate
	pricing   *metering.PricingDirectory
	timeouts  Timeouts
	pipeline  *metering.Pipeline
	pools     map[string]*keypool.Pool
	tpm       *ratelimit.TPM
	cache     *semantic.Cache
	tenantSw  func(string) bool
	caps      map[governKey]config.ModelCapability
	strictGov bool
	plugins   *plugin.Registry
	randIntn  func(int) int
	logger    *slog.Logger
	client    *http.Client
}

// New 构造代理:路由候选按 priority 升序排序;HTTP 客户端带拨号层超时与连接池。
func New(cfg Config) *Proxy {
	sorted := make(map[string][]config.Candidate, len(cfg.Routes))
	for alias, cands := range cfg.Routes {
		cs := append([]config.Candidate(nil), cands...)
		for i := 1; i < len(cs); i++ { // 插入排序:候选列表极短
			for j := i; j > 0 && cs[j].Priority < cs[j-1].Priority; j-- {
				cs[j], cs[j-1] = cs[j-1], cs[j]
			}
		}
		sorted[alias] = cs
	}
	transport := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		DialContext:         (&net.Dialer{Timeout: cfg.Timeouts.Dial, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout: cfg.Timeouts.Dial,
		// 自定义 DialContext 会关闭 net/http 的自动 h2 升级,须显式开启(上游 HTTPS 多路复用)
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
	caps := make(map[governKey]config.ModelCapability, len(cfg.Capabilities))
	for _, mc := range cfg.Capabilities {
		caps[governKey{mc.Provider, mc.Model}] = mc
	}
	return &Proxy{
		registry:  cfg.Registry,
		routes:    sorted,
		pricing:   cfg.Pricing,
		timeouts:  cfg.Timeouts,
		pipeline:  cfg.Pipeline,
		pools:     cfg.Pools,
		tpm:       cfg.TPM,
		cache:     cfg.Cache,
		tenantSw:  cfg.TenantSwitch,
		plugins:   cfg.Plugins,
		caps:      caps,
		strictGov: cfg.StrictGovernance,
		randIntn:  randIntn,
		logger:    logger,
		client:    &http.Client{Transport: transport},
	}
}

// Handler 返回挂载到 mux 的入口(POST /v1/chat/completions)。
func (p *Proxy) Handler() http.Handler {
	return http.HandlerFunc(p.handleChat)
}

// cacheTenantEnabled 查租户语义缓存开关(装配层注入 DB 查询闭包)。
func (p *Proxy) cacheTenantEnabled(tenantID string) bool {
	if p.tenantSw == nil {
		return false
	}
	return p.tenantSw(tenantID)
}

// trackWriter 记录响应是否已向客户端提交(写过头或体)。候选循环据此终结:
// attempt 内部已写出的信封(早退路径)或断言出口之后,绝不允许再次换道写出,
// 否则客户端会收到双信封拼接或「500 头 + 成功体」的语义矛盾响应。
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

// Flush 转发底层 Flusher(SSE 逐事件冲刷契约;勿让本包装截断能力探测)。
func (t *trackWriter) Flush() {
	if f, ok := t.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap 支持 http.ResponseController 穿透包装。
func (t *trackWriter) Unwrap() http.ResponseWriter { return t.ResponseWriter }

// handleChat 生命周期 7-9 步:解析校验 → 路由 → 候选循环(当前单候选)。
func (p *Proxy) handleChat(w http.ResponseWriter, r *http.Request) {
	tw := &trackWriter{ResponseWriter: w} // 提交追踪:已写出的响应绝不二次写出/换道
	body, err := io.ReadAll(http.MaxBytesReader(tw, r.Body, maxInboundBody))
	if err != nil {
		p.writeError(tw, r, protocol.TypeBadRequest, "bad_request", "请求体过大或不可读")
		return
	}
	req, err := protocol.ParseChatRequest(body)
	if err != nil {
		p.writeError(tw, r, protocol.TypeBadRequest, "bad_request", "请求不符合 OpenAI Chat Completions 格式")
		return
	}
	cands, ok := p.routes[req.Model]
	if !ok || len(cands) == 0 {
		p.writeError(tw, r, protocol.TypeBadRequest, "model_not_found", "模型别名不存在: "+req.Model)
		return
	}

	// 插件请求体钩子(ADR-0011):单次执行,先于语义缓存检索——脱敏必须发生在
	// 缓存键计算之前,敏感载荷不得进语义缓存;改写未发生不做重解析(零开销路径)
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
			if cands, ok = p.routes[req.Model]; !ok || len(cands) == 0 {
				p.writeError(tw, r, protocol.TypeBadRequest, "model_not_found", "模型别名不存在: "+req.Model)
				return
			}
			p.markPluginModified(tw, r, res.ModifiedBy)
		}
	}

	// 生命周期第 8 步:构造候选迭代器(同 priority 加权随机起点,priority 递增降级)
	fcs := make([]failover.Candidate, len(cands))
	for i, c := range cands {
		fcs[i] = failover.Candidate{Provider: c.Provider, Model: c.Model, Priority: c.Priority, Weight: c.Weight}
	}
	// 语义缓存检索(T4.5):命中即回放并计量,未命中记住相位供出站成功后入库
	hitStart := time.Now()
	res, cph := p.lookupCache(r, req)
	if res != nil {
		p.recordCacheHit(r, req, res, hitStart)
		if req.Stream {
			p.replayStream(tw, r, res)
		} else {
			p.replayNonStream(tw, r, res)
		}
		return
	}

	iter := failover.NewIterator(fcs, p.randIntn)
	var last failover.ErrorClass
	for {
		cand, ok := iter.Next()
		if !ok {
			// 候选耗尽(proxy-and-streaming):code=all_candidates_failed,
			// 类型沿用最后错误映射,message 如实说明已尝试全部候选。
			// 能走到耗尽,说明每个候选都未对客户端产出(decide=next_candidate),信封可安全写出
			if tw.committed {
				return
			}
			typ := classToType(last)
			p.writeError(tw, r, typ, "all_candidates_failed",
				"上游供应商暂时不可用,已尝试全部候选(最后错误: "+string(last)+")")
			return
		}

		// 第 9 步:取 key → 执行 → 按 Decide 动作分支 → 结果回写 key 池
		pool := p.pools[cand.Provider]
		key := pool.Pick(time.Now())
		if key == nil {
			// 该 provider 全部 key 冷却/禁用:跳过候选,不计错误类
			p.logger.Warn("provider key 池不可用,跳过候选",
				"request_id", loggingRequestID(r.Context()), "provider", cand.Provider)
			continue
		}
		m := &meterState{start: time.Now(), status: "ok"}
		handled, class := p.attempt(tw, r, req, cand, key, m)
		if pool != nil {
			p.reportKeyOutcome(pool, key, class, handled, m)
		}
		if class == "" {
			p.storePhase(r, cph, req, cand, m.outBody)
			return
		}
		// attempt 已对客户端产出(早退信封/断言出口):请求终结,绝不换道重写
		if tw.committed {
			return
		}
		switch failover.Decide(class, m.firstSet, req.Stream) {
		case failover.ActionNextCandidate:
			last = class
			continue
		case failover.ActionReturn:
			// 不换道直接返回(参数问题换家也会拒):归一信封携带上游可读 message
			p.writeError(tw, r, classToType(class), string(class), m.upstreamMsg)
			return
		case failover.ActionSilent, failover.ActionAbort:
			// 静默退出 / 断言出口已写出,不再有任何输出
			return
		}
	}
}

// govern 按目标候选解析能力约束并执行治理(目录未声明 → 原件零干预返回)。
func (p *Proxy) govern(req *protocol.ChatRequest, cand failover.Candidate) (*protocol.ChatRequest, []governChange, string) {
	capability, ok := p.caps[governKey{cand.Provider, cand.Model}]
	if !ok {
		return req, nil, ""
	}
	return governChat(req, capability, p.strictGov)
}

// traceGovern 留痕双通道(ADR-0010):结构化日志逐条明细 + 客户端可见响应头参数清单。
// 头在响应提交前写入(此处位于任何 WriteHeader 之前);换道后无干预即清除上一目标残留。
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

// attempt 单候选执行:按流式与否分发,并在退出时计量第 11 步
// (成功/失败/断连三态都记;defer 保证任何出口都落账)。
func (p *Proxy) attempt(w http.ResponseWriter, r *http.Request, req *protocol.ChatRequest, cand failover.Candidate, key *keypool.Key, m *meterState) (handled bool, class failover.ErrorClass) {
	ad, ok := p.registry.Get(cand.Provider)
	if !ok {
		// config 校验保证 provider 已注册;防御性兜底
		p.logger.Error("provider 未注册", "provider", cand.Provider)
		p.writeError(w, r, protocol.TypeInternal, "adapter_missing", "内部配置错误: 适配器未注册")
		return true, failover.ClassUnknown
	}
	// 请求参数治理(ADR-0010):每次 attempt 从原始请求出发按目标 (provider, model) 重新治理——
	// 换道后不得沿用上一目标的收敛结果(spec 换道重治理场景);strict 命中即 400 终结。
	greq, gchanges, greject := p.govern(req, cand)
	if greject != "" {
		p.writeError(w, r, protocol.TypeBadRequest, "param_rejected", greject)
		return true, failover.ClassBadRequest
	}
	p.traceGovern(w, r, cand, gchanges)
	out, err := ad.Prepare(greq, cand.Model, key.Value)
	if err != nil {
		p.writeError(w, r, protocol.TypeBadRequest, "bad_request", "出站请求构造失败")
		return true, failover.ClassBadRequest
	}

	// 计量第 11 步:任何出口统一落账(defer)
	defer func() {
		inputText, _ := json.Marshal(req.Messages)
		p.record(r, req.Model, cand.Model, cand, string(inputText), m)
	}()

	if req.Stream {
		handled, class := p.attemptStream(w, r, ad, out, m)
		if class == "" && m.outBody == nil {
			// 流式成功:按累计内容拼包标准响应体供语义缓存入库(非 stop 终态返回 nil 不入库)
			m.outBody = m.assembleStreamBody(cand.Model)
		}
		return handled, class
	}
	return p.attemptNonStream(w, r, ad, out, m)
}

// outboundRequest 组装上游请求:context 链断连传导的生命线(conventions)。
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

// writeError 唯一错误出口(conventions 信封 + 状态映射表,T2.7 契约)。
func (p *Proxy) writeError(w http.ResponseWriter, r *http.Request, typ, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(protocol.StatusForType(typ))
	_ = json.NewEncoder(w).Encode(protocol.NewErrorEnvelope(typ, code, msg, logging.RequestIDFrom(r.Context())))
}

// classToType 判定表 ErrorClass → conventions 错误类型(状态映射前一步)。
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

// upstreamMessage 尽力抽取上游错误 body 中人类可读 message(供 400 等终结错误透传);失败给缺省。
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

// loggingRequestID ctx 取 request_id 的包内别名(日志字段统一走 conventions 表)。
func loggingRequestID(ctx context.Context) string {
	return logging.RequestIDFrom(ctx)
}
