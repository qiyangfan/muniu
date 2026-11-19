// 拓扑以不可变快照原子替换；每个请求始终使用入口处加载的同一快照。
// 校验或构建失败时保留旧快照；provider 配置未变时复用原 key 池及其运行状态。
package proxy

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/solvela/muniu/internal/adapter"
	"github.com/solvela/muniu/internal/config"
	"github.com/solvela/muniu/internal/keypool"
)

// topologySnapshot 保存一次拓扑快照及其对应的池、适配器和能力声明。
// 替换时整体切换，保证在途请求不会混用新旧配置；loadedAt 用于模型目录的 created 字段。
type topologySnapshot struct {
	providers   []config.Provider             // 原始定义，池保留的 DeepEqual 基准
	routes      map[string][]config.Candidate // priority 升序已排序
	pools       map[string]*keypool.Pool
	adapters    map[string]adapter.Adapter // provider 到出站适配器，与池同生命周期
	clients     map[string]*http.Client    // provider 到声明代理的出站 client（缺项 = 默认 client，env 语义）
	caps        map[governKey]config.ModelCapability
	nativeResps map[string]bool   // provider 到 native_responses 声明，responses 直通资格，声明制
	authModes   map[string]string // provider 到 auth 声明(oauth 记录用,lane 收窄判定)
	loadedAt    time.Time
}

// TopologyStats 记录一次拓扑替换中 key 池的复用、创建和移除数量。
type TopologyStats struct {
	Reused  int // 配置未变，运行态无损保留
	Created int // 新增或配置变更，池重建
	Removed int // 拓扑中消失，池丢弃，在途请求持旧池完成
}

// Topology 返回当前快照的 provider 和 key 池。
func (p *Proxy) Topology() ([]config.Provider, map[string]*keypool.Pool) {
	s := p.topo.Load()
	if s == nil {
		return nil, nil
	}
	return s.providers, s.pools
}

// ReplaceTopology 校验并原子替换拓扑。cfg 必须包含 Providers、Routes 和 Capabilities。
// buildPool 只为新增或已变更的 provider 调用；任何校验或构建错误都会保留旧快照。
func (p *Proxy) ReplaceTopology(cfg Config,
	buildPool func(config.Provider) (*keypool.Pool, adapter.Adapter, error)) (TopologyStats, error) {
	if len(cfg.Routes) == 0 {
		return TopologyStats{}, errors.New("拓扑替换被拒绝: model_routes 为空(下线全部路由不合法)")
	}
	if buildPool == nil {
		return TopologyStats{}, errors.New("拓扑替换被拒绝: 池构建回调缺失")
	}
	old := p.topo.Load()
	if err := p.validateTopology(cfg); err != nil {
		return TopologyStats{}, err
	}

	// 旧声明代理 URL 表:控制面拓扑载荷不含 proxy_url 字段(表达力缺口),InheritProxy=true
	// 时入站声明为空的渠道沿用旧值并物化进新快照——否则控制面首次同步即静默摘除代理,
	// 且探针面与数据面各读各源(劈叉)。配置文件源(SIGHUP)不走沿用:用户从 yaml 删除
	// proxy_url 是显式清空,沿用会吞掉变更(清代理在配置源语义下即时生效)。
	oldProxyBy := make(map[string]string, len(old.providers))
	for _, prev := range old.providers {
		oldProxyBy[prev.Name] = prev.ProxyURL
	}
	stats := TopologyStats{}
	oldByName := providerByName(old)
	prevByName := providerByName(old)
	b := &topologyBuild{old: old, oldProxyBy: oldProxyBy, buildPool: buildPool, dialTimeout: p.timeouts.Dial,
		reuse: map[string]reuseEntry{}, next: map[string]reuseEntry{}, prevByName: prevByName}
	for _, prov := range cfg.Providers {
		delete(oldByName, prov.Name) // 先出表：循环后剩余即旧有新无
	}
	providersOut, err := b.buildProviders(cfg, &stats)
	if err != nil {
		return TopologyStats{}, err
	}
	// 旧有新无：池与适配器随旧快照释放，在途请求持旧池完成
	stats.Removed = len(oldByName)
	for name, oc := range old.clients {
		if _, gone := oldByName[name]; gone {
			oc.CloseIdleConnections() // 拓扑中消失的渠道代理 client：关空闲连接防泄漏
		}
	}

	p.topo.Store(&topologySnapshot{
		providers:   providersOut,
		routes:      sortRoutes(cfg.Routes),
		pools:       b.pools(),
		adapters:    b.adapters(),
		clients:     b.clients(),
		caps:        buildCaps(cfg.Capabilities),
		nativeResps: buildNativeResps(cfg.Providers),
		authModes:   buildAuthModes(cfg.Providers),
		loadedAt:    time.Now(),
	})
	return stats, nil
}

// validateTopology 拓扑校验：auth 声明共享校验、渠道 URL/权重/优先级与路由数值域、池覆盖。
func (p *Proxy) validateTopology(cfg Config) error {
	// auth/账号声明共享校验(与 config.Load 同源 AuthDeclarationViolations):控制面拓扑
	// 载荷不经 YAML 校验,缺此门则值域契约只在文件源生效,跨源行为劈叉(评审域一 P1);
	// 含渠道内重复账号引用拒绝(凭据同步只达首个同 ID 成员,其余成员冻结)。
	if vs := config.AuthDeclarationViolations(cfg.Providers, p.oauthCfg); len(vs) > 0 {
		return fmt.Errorf("拓扑替换被拒绝: auth 声明非法: %s", strings.Join(vs, "; "))
	}
	// 拓扑可能来自控制面，须限制在本机 int 可安全处理的数值范围内。
	for _, prov := range cfg.Providers {
		if err := validateProviderNums(prov); err != nil {
			return err
		}
	}
	if err := validateRouteNums(cfg.Routes); err != nil {
		return err
	}
	known := map[string]bool{}
	for _, prov := range cfg.Providers {
		known[prov.Name] = true
	}
	if missing := checkPoolCoverage(cfg.Routes, known); len(missing) > 0 {
		return fmt.Errorf("拓扑替换被拒绝: 候选引用了未定义 provider %v", missing)
	}
	return nil
}

// validateProviderNums 单渠道 URL、权重与优先级校验。
func validateProviderNums(prov config.Provider) error {
	if prov.NativeMultimodalEmbeddingsURL != "" && !validMultimodalURL(prov) {
		return fmt.Errorf("拓扑替换被拒绝: provider %q native_multimodal_embeddings_url 非法或适配器不支持", prov.Name)
	}
	if prov.ProxyURL != "" {
		if err := validateProxyURL(prov); err != nil {
			return err
		}
	}
	if prov.Weight <= 0 || int64(prov.Weight) > config.MaxTopologyInt {
		return fmt.Errorf(
			"拓扑替换被拒绝: provider %q weight 必须 >0 且 ≤%d, 实际 %d", prov.Name, config.MaxTopologyInt, prov.Weight)
	}
	if prov.Priority < 0 || int64(prov.Priority) > config.MaxTopologyInt {
		return fmt.Errorf(
			"拓扑替换被拒绝: provider %q priority 必须在 [0,%d], 实际 %d", prov.Name, config.MaxTopologyInt, prov.Priority)
	}
	return nil
}

// validMultimodalURL 原生多模态向量端点仅 qwen 适配器支持，须为无 fragment 的 http(s) 绝对 URL。
func validMultimodalURL(prov config.Provider) bool {
	u, err := url.Parse(prov.NativeMultimodalEmbeddingsURL)
	return prov.Adapter == "qwen" && err == nil &&
		(u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.Fragment == ""
}

// validateProxyURL 渠道出站代理声明校验；拒绝文案不回显内嵌凭证。
func validateProxyURL(prov config.Provider) error {
	u, err := url.Parse(prov.ProxyURL)
	if err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" {
		return nil
	}
	redacted := prov.ProxyURL
	if err == nil {
		redacted = u.Redacted() // 允许内嵌凭证,拒绝文案不回显原文
	}
	return fmt.Errorf("拓扑替换被拒绝: provider %q proxy_url 非法（须为 http/https 绝对 URL）: %q",
		prov.Name, redacted)
}

// validateRouteNums 路由候选优先级与权重数值域校验。
func validateRouteNums(routes map[string][]config.Candidate) error {
	for alias, cands := range routes {
		for _, c := range cands {
			if c.Priority < 0 || int64(c.Priority) > config.MaxTopologyInt {
				return fmt.Errorf(
					"拓扑替换被拒绝: model_routes[%q] priority 必须在 [0,%d], 实际 %d", alias, config.MaxTopologyInt, c.Priority)
			}
			if int64(c.Weight) > config.MaxTopologyInt {
				return fmt.Errorf(
					"拓扑替换被拒绝: model_routes[%q] weight 超出数值域(≤%d)", alias, config.MaxTopologyInt)
			}
		}
	}
	return nil
}

// reuseEntry 复用或新建渠道的三件套：池、适配器与声明代理 client。
type reuseEntry struct {
	pool   *keypool.Pool
	ad     adapter.Adapter
	client *http.Client
}

// topologyBuild 拓扑替换的构建期状态。
type topologyBuild struct {
	old         *topologySnapshot
	oldProxyBy  map[string]string
	buildPool   func(config.Provider) (*keypool.Pool, adapter.Adapter, error)
	dialTimeout time.Duration
	prevByName  map[string]config.Provider // 旧快照渠道定义，配置未变判定基准
	reuse       map[string]reuseEntry      // 配置未变直接复用
	next        map[string]reuseEntry      // 新建或配置变更后重建
}

func (b *topologyBuild) pools() map[string]*keypool.Pool {
	out := make(map[string]*keypool.Pool, len(b.reuse)+len(b.next))
	for k, v := range b.reuse {
		out[k] = v.pool
	}
	for k, v := range b.next {
		out[k] = v.pool
	}
	return out
}

func (b *topologyBuild) adapters() map[string]adapter.Adapter {
	out := make(map[string]adapter.Adapter, len(b.reuse)+len(b.next))
	for k, v := range b.reuse {
		out[k] = v.ad
	}
	for k, v := range b.next {
		out[k] = v.ad
	}
	return out
}

func (b *topologyBuild) clients() map[string]*http.Client {
	out := make(map[string]*http.Client, len(b.reuse)+len(b.next))
	for k, v := range b.reuse {
		if v.client != nil {
			out[k] = v.client
		}
	}
	for k, v := range b.next {
		if v.client != nil {
			out[k] = v.client
		}
	}
	return out
}

// buildProviders 逐渠道构建：配置未变复用运行态，变更则经 buildPool 重建并迁移代理 client。
func (b *topologyBuild) buildProviders(cfg Config, stats *TopologyStats) ([]config.Provider, error) {
	providersOut := make([]config.Provider, len(cfg.Providers))
	for i := range cfg.Providers {
		prov := cfg.Providers[i]
		if cfg.InheritProxy && prov.ProxyURL == "" && b.oldProxyBy[prov.Name] != "" {
			prov.ProxyURL = b.oldProxyBy[prov.Name]
		}
		providersOut[i] = prov
		if prev, ok := b.prevByName[prov.Name]; ok && providersEqual(prev, prov) {
			b.reuse[prov.Name] = reuseEntry{pool: b.old.pools[prov.Name], ad: b.old.adapters[prov.Name],
				client: b.old.clients[prov.Name]}
			stats.Reused++
			continue
		}
		e, err := b.rebuild(prov)
		if err != nil {
			return nil, err
		}
		b.next[prov.Name] = e
		stats.Created++
	}
	return providersOut, nil
}

// rebuild 新建或配置变更渠道：重建池与适配器，代理 URL 未变且旧 client 在则复用(连接池无损)，
// 其余场景(新建代理/换代理/清空代理)关旧空闲连接,防换道后泄漏。
func (b *topologyBuild) rebuild(prov config.Provider) (reuseEntry, error) {
	pool, ad, err := b.buildPool(prov)
	if err != nil {
		return reuseEntry{}, fmt.Errorf("拓扑替换被拒绝: provider %q 构建失败: %w", prov.Name, err)
	}
	e := reuseEntry{pool: pool, ad: ad}
	if oc := b.old.clients[prov.Name]; oc != nil {
		if prov.ProxyURL != "" && b.oldProxyBy[prov.Name] == prov.ProxyURL {
			e.client = oc
		} else {
			oc.CloseIdleConnections()
		}
	}
	if prov.ProxyURL != "" && e.client == nil {
		cl, cerr := ProxiedClient(prov.ProxyURL, b.dialTimeout)
		if cerr != nil {
			return reuseEntry{}, fmt.Errorf("拓扑替换被拒绝: provider %q proxy_url 非法: %w", prov.Name, cerr)
		}
		e.client = cl
	}
	return e, nil
}

// checkPoolCoverage 返回路由引用但未定义的 provider。
func checkPoolCoverage(routes map[string][]config.Candidate, known map[string]bool) []string {
	var missing []string
	seen := map[string]bool{}
	for _, cands := range routes {
		for _, c := range cands {
			if !known[c.Provider] && !seen[c.Provider] {
				seen[c.Provider] = true
				missing = append(missing, c.Provider)
			}
		}
	}
	return missing
}

func providerByName(s *topologySnapshot) map[string]config.Provider {
	out := map[string]config.Provider{}
	if s == nil {
		return out
	}
	for _, p := range s.providers {
		out[p.Name] = p
	}
	return out
}

// providersEqual 比较 provider 的拓扑配置（含出站代理声明与鉴权声明），包括 key 引用列表。
// proxy_url 必须入比较：SIGHUP 仅改代理声明时，复用分支会让变更静默失效、探针面与
// 数据面各读各源劈叉（评审 A1）。auth 同理:声明变更(bearer↔oauth)不改适配器即静默吞掉。
func providersEqual(a, b config.Provider) bool {
	return a.Name == b.Name && a.Adapter == b.Adapter && a.BaseURL == b.BaseURL &&
		a.NativeMultimodalEmbeddingsURL == b.NativeMultimodalEmbeddingsURL &&
		a.ProxyURL == b.ProxyURL && a.Auth == b.Auth &&
		a.Weight == b.Weight && a.Priority == b.Priority && equalKeys(a.Keys, b.Keys)
}

// equalKeys key 引用列表按序逐项相等。
func equalKeys(a, b []config.KeyRef) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// buildAuthModes provider 到 auth 声明映射(oauth lane 收窄判定用;非 oauth 渠道不入表)。
func buildAuthModes(provs []config.Provider) map[string]string {
	m := map[string]string{}
	for _, p := range provs {
		if p.Auth == adapter.AuthOAuth {
			m[p.Name] = p.Auth
		}
	}
	return m
}

// sortRoutes 按 priority 升序排列每个路由的候选。
func sortRoutes(routes map[string][]config.Candidate) map[string][]config.Candidate {
	sorted := make(map[string][]config.Candidate, len(routes))
	for alias, cands := range routes {
		cs := append([]config.Candidate(nil), cands...)
		for i := 1; i < len(cs); i++ { // 插入排序，候选列表极短
			for j := i; j > 0 && cs[j].Priority < cs[j-1].Priority; j-- {
				cs[j], cs[j-1] = cs[j-1], cs[j]
			}
		}
		sorted[alias] = cs
	}
	return sorted
}

func buildCaps(caps []config.ModelCapability) map[governKey]config.ModelCapability {
	m := make(map[governKey]config.ModelCapability, len(caps))
	for _, mc := range caps {
		m[governKey{mc.Provider, mc.Model}] = mc
	}
	return m
}
