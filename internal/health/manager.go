package health

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// rfc3339 状态快照时刻序列化(秒级 UTC)。
func rfc3339(t time.Time) string { return t.UTC().Truncate(time.Second).Format(time.RFC3339) }

// ratioText 毫单位比率人类文案(667 → "66.7%")。
func ratioText(milli int64) string { return fmt.Sprintf("%.1f%%", float64(milli)/10) }

// boolToInt 布尔计数。
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Channel 渠道探针运行态(装配层从拓扑快照构建,运行期动态读取感知热替换)。
type Channel struct {
	Name       string
	Adapter    string
	BaseURL    string
	Auth       string       // anthropic 桥接鉴权形态;空 = 缺省 x-api-key
	ProbeModel string       // 非空 = 启用付费推理探针(声明制)
	FreeProbe  bool         // 免费控制面探针开关(装配期已按适配器能力与退订声明归一)
	Outbound   *http.Client // 渠道声明出站代理 client(装配期按 proxy_url 解析);nil = 默认 client(env 语义)
	Keys       []ChannelKey
}

// ChannelKey 渠道 key 取值(装配层从 keypool 快照映射,health 不依赖 keypool)。
type ChannelKey struct {
	ID    string
	Value string
}

// Options 健康管理器装配参数;零值经 NewManager 兜底缺省。
type Options struct {
	Interval         time.Duration // 探针间隔,缺省 300s
	Timeout          time.Duration // 探针单次超时,缺省 10s
	FailureThreshold int           // 连续失败阈值,缺省 2
	OpenFor          time.Duration // 熔断时长(degraded 计时),缺省 300s
	SwitchWindow     time.Duration // 别名换道率窗口,缺省 300s
	SwitchMinSamples int           // 换道率最低样本数,缺省 20
	SwitchRateMilli  int64         // 换道率越线阈值,缺省 300
	Logger           *slog.Logger
	Now              func() time.Time
	Client           *http.Client // 探针 HTTP 客户端(测试注入)
	// Sources 运行期渠道拓扑(感知热替换):渠道名 → 探针参数与 key 取值。
	Sources func() map[string]Channel
	// Emit 状态迁移事件出口(装配层转 alerting 管道);nil = 仅落状态不通知。
	Emit func(Event)
}

// Manager 渠道健康管理器:探针循环 + 被动统计入口 + 状态机持有者。
// 循环纪律照 balance Service 先例:启动即首轮、间隔推进、单渠道 panic 隔离、优雅停止。
type Manager struct {
	opts   Options
	log    *slog.Logger
	ctx    context.Context
	cancel context.CancelFunc
	stop   chan struct{}
	done   chan struct{}

	mu           sync.Mutex
	states       map[string]*ProviderState
	aliasWins    map[string]*RingWindow // 别名换道率窗口
	aliasFiring  map[string]bool        // switch_rate_exceeded 触发态(单次发射/回落解除)
	providerWins map[string]*RingWindow // 渠道被动失败率窗口
	client       *http.Client
}

// NewManager 构造并兜底缺省;调用方随后 Start()。
func NewManager(opts Options) *Manager {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Interval <= 0 {
		opts.Interval = 5 * time.Minute
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 10 * time.Second
	}
	if opts.FailureThreshold <= 0 {
		opts.FailureThreshold = 2
	}
	if opts.OpenFor <= 0 {
		opts.OpenFor = 5 * time.Minute
	}
	if opts.SwitchWindow <= 0 {
		opts.SwitchWindow = 5 * time.Minute
	}
	if opts.SwitchMinSamples <= 0 {
		opts.SwitchMinSamples = 20
	}
	if opts.SwitchRateMilli <= 0 {
		opts.SwitchRateMilli = 300
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Client == nil {
		opts.Client = &http.Client{Timeout: opts.Timeout}
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{
		opts: opts, log: opts.Logger, ctx: ctx, cancel: cancel,
		stop: make(chan struct{}), done: make(chan struct{}),
		states:       map[string]*ProviderState{},
		aliasWins:    map[string]*RingWindow{},
		aliasFiring:  map[string]bool{},
		providerWins: map[string]*RingWindow{},
		client:       opts.Client,
	}
}

// Start 启动探针循环(启动即首轮,不等首个间隔)。
func (m *Manager) Start() { go m.loop() }

// Close 优雅停止。
func (m *Manager) Close() {
	close(m.stop)
	m.cancel()
	select {
	case <-m.done:
	case <-time.After(m.opts.Timeout + 2*time.Second):
		m.log.Warn("健康探针循环停止超时,继续退出")
	}
}

func (m *Manager) loop() {
	defer close(m.done)
	m.tick()
	ticker := time.NewTicker(m.opts.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-m.stop:
			return
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			m.tick()
		}
	}
}

// tick 一轮探针:逐渠道免费/付费探针 + open 到期转半开推进;单渠道 panic 隔离。
func (m *Manager) tick() {
	if m.opts.Sources == nil {
		return
	}
	for name, ch := range m.opts.Sources() {
		func() {
			defer func() {
				if r := recover(); r != nil {
					m.log.Error("健康探针 panic(已按渠道隔离)", "provider", name, "error_detail", fmtSprint(r))
				}
			}()
			if ch.FreeProbe && ProbeSupported(ch.Adapter) {
				oc, class := m.freeProbe(ch)
				m.applyProbe(name, oc, class, ReasonControlPlane)
			}
			if ch.ProbeModel != "" && ProbeSupported(ch.Adapter) { // 不支持探针的适配器族静默跳过(登记限制),防每轮失败计数误熔断
				oc, class := m.inferenceProbe(ch)
				m.applyProbe(name, oc, class, ReasonInference)
			}
			m.advanceTick(name)
		}()
	}
	// 状态 GC:热替换移除的渠道不再有任何信号触达,超期清理,防幽灵渠道永驻管理面。
	// 纯按信号新鲜度清理(不比對 Sources):热替换新增渠道靠真实流量触达保持,被动统计不受影响。
	now := m.opts.Now()
	staleAfter := 10 * m.opts.Interval
	m.mu.Lock()
	for name, st := range m.states {
		if now.Sub(st.LastActiveAt) >= staleAfter {
			delete(m.states, name)
			delete(m.providerWins, name) // 窗口与状态同生命周期
		}
	}
	m.mu.Unlock()
}

func fmtSprint(v any) string { return fmt.Sprint(v) }

// applyProbe 状态机推进 + 事件出口(探针侧)。
func (m *Manager) applyProbe(name string, oc ProbeOutcome, class string, layer Reason) {
	now := m.opts.Now()
	m.mu.Lock()
	st := m.stateOf(name)
	ns, ev := applyProbe(*st, oc, class, layer, now, m.opts.FailureThreshold, m.opts.OpenFor)
	m.states[name] = &ns
	m.mu.Unlock()
	m.emitEvent(ev)
}

// advanceTick open 计时到期转半开(无事件)。
func (m *Manager) advanceTick(name string) {
	now := m.opts.Now()
	m.mu.Lock()
	st := m.stateOf(name)
	ns := applyTick(*st, now)
	m.states[name] = &ns
	m.mu.Unlock()
}

// stateOf 取或初始化渠道状态(调用方持锁)。
func (m *Manager) stateOf(name string) *ProviderState {
	if st, ok := m.states[name]; ok {
		return st
	}
	st := &ProviderState{Provider: name}
	m.states[name] = st
	return st
}

// Contracted 渠道是否处于降级收缩态(候选循环据此整路跳过);nil 管理器恒 false。
func (m *Manager) Contracted(name string) bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	st, ok := m.states[name]
	return ok && st.Contracted()
}

// TrialResult 半开期真实请求试探回填(非半开态 no-op);ok = 该次尝试成功。
func (m *Manager) TrialResult(name string, ok bool) {
	if m == nil {
		return
	}
	now := m.opts.Now()
	m.mu.Lock()
	st := m.stateOf(name)
	ns, ev := applyTrafficTrial(*st, ok, now, m.opts.OpenFor)
	m.states[name] = &ns
	m.mu.Unlock()
	m.emitEvent(ev)
}

// AliasRequest 别名请求计数(每请求一次,换道率分母)。
func (m *Manager) AliasRequest(alias string) { m.aliasAdd(alias, 1, 0) }

// AliasSwitch 别名换道计数(每次换道一次,分子)。
func (m *Manager) AliasSwitch(alias string) { m.aliasAdd(alias, 0, 1) }

func (m *Manager) aliasAdd(alias string, total, hit int) {
	if m == nil {
		return
	}
	now := m.opts.Now()
	m.mu.Lock()
	win, ok := m.aliasWins[alias]
	if !ok {
		win = NewRingWindow(m.opts.SwitchWindow, m.opts.SwitchMinSamples, m.opts.SwitchRateMilli)
		m.aliasWins[alias] = win
	}
	breached := win.Add(total, hit, now)
	firing := m.aliasFiring[alias]
	switch {
	case breached && !firing:
		m.aliasFiring[alias] = true
		ratio, _, _ := win.Snapshot(now)
		m.mu.Unlock()
		m.emit(&Event{
			Kind: "switch_rate_exceeded", Provider: alias, At: now,
			Summary: "别名 " + alias + " 窗口内换道率越线(" + ratioText(ratio) + "),上游可能部分劣化",
		})
		return
	case !breached && firing:
		m.aliasFiring[alias] = false // 回落解除,重新武装
	}
	m.mu.Unlock()
}

// ProviderTraffic 渠道真实流量结果计数(被动失败率窗口);每 attempt 一次。
func (m *Manager) ProviderTraffic(name string, failed bool) {
	if m == nil {
		return
	}
	now := m.opts.Now()
	m.mu.Lock()
	win, ok := m.providerWins[name]
	if !ok {
		win = NewRingWindow(m.opts.SwitchWindow, m.opts.SwitchMinSamples, m.opts.SwitchRateMilli)
		m.providerWins[name] = win
	}
	breached := win.Add(1, boolToInt(failed), now)
	st := m.stateOf(name)
	ns, ev := applyPassive(*st, breached, now, m.opts.OpenFor)
	m.states[name] = &ns
	m.mu.Unlock()
	m.emitEvent(ev)
}

func (m *Manager) emitEvent(ev *Event) { m.emit(ev) }

func (m *Manager) emit(ev *Event) {
	if ev == nil || m.opts.Emit == nil {
		return
	}
	m.opts.Emit(*ev)
}

// ProviderView 管理面健康总览单渠道形态。
type ProviderView struct {
	Provider       string `json:"provider"`
	Phase          string `json:"phase"`
	Reason         string `json:"reason,omitempty"`
	FreeProbe      bool   `json:"free_probe"`            // 渠道免费控制面探针是否生效(适配器能力与退订声明归一后)
	ProbeModel     string `json:"probe_model,omitempty"` // 非空 = 付费推理探针已启用(声明制)
	DegradedAt     string `json:"degraded_at,omitempty"`
	OpenUntil      string `json:"open_until,omitempty"`
	RecoveredAt    string `json:"recovered_at,omitempty"`
	LastProbeAt    string `json:"last_probe_at,omitempty"`
	LastProbeOK    *bool  `json:"last_probe_ok"`
	LastProbeClass string `json:"last_probe_class,omitempty"`
}

// AliasView 管理面换道率单别名形态。
type AliasView struct {
	Alias      string `json:"alias"`
	RatioMilli int64  `json:"ratio_milli"`
	Samples    int    `json:"samples"`
	Breached   bool   `json:"breached"`
}

// Snapshot 管理面总览(相位/探针配置与结果/换道率);restart 后零态如实呈现。
func (m *Manager) Snapshot() ([]ProviderView, []AliasView) {
	if m == nil {
		return nil, nil
	}
	now := m.opts.Now()
	// 探针配置取自运行期渠道拓扑(锁外取,配置源自持锁纪律);渠道已移除但状态未过期时归零值。
	var srcs map[string]Channel
	if m.opts.Sources != nil {
		srcs = m.opts.Sources()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	providers := make([]ProviderView, 0, len(m.states))
	for _, st := range m.states {
		v := ProviderView{
			Provider: st.Provider, Phase: st.Phase.String(), LastProbeClass: st.LastProbeClass,
		}
		if ch, ok := srcs[st.Provider]; ok {
			v.FreeProbe = ch.FreeProbe
			v.ProbeModel = ch.ProbeModel
		}
		if st.Phase != PhaseHealthy {
			v.Reason = st.Reason.String()
		}
		if !st.DegradedAt.IsZero() {
			v.DegradedAt = rfc3339(st.DegradedAt)
		}
		if st.Phase == PhaseDegraded && !st.OpenUntil.IsZero() {
			v.OpenUntil = rfc3339(st.OpenUntil)
		}
		if !st.RecoveredAt.IsZero() {
			v.RecoveredAt = rfc3339(st.RecoveredAt)
		}
		if st.HasProbe {
			v.LastProbeAt = rfc3339(st.LastProbeAt)
			ok := st.LastProbeOK
			v.LastProbeOK = &ok
		}
		providers = append(providers, v)
	}
	aliases := make([]AliasView, 0, len(m.aliasWins))
	for alias, win := range m.aliasWins {
		ratio, samples, breached := win.Snapshot(now)
		aliases = append(aliases, AliasView{Alias: alias, RatioMilli: ratio, Samples: samples, Breached: breached})
	}
	return providers, aliases
}
