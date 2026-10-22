// 拓扑以不可变快照原子替换；每个请求始终使用入口处加载的同一快照。
// 校验或构建失败时保留旧快照；provider 配置未变时复用原 key 池及其运行状态。
package proxy

import (
	"errors"
	"fmt"
	"net/url"
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
	caps        map[governKey]config.ModelCapability
	nativeResps map[string]bool // provider 到 native_responses 声明，responses 直通资格，声明制
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
	// 拓扑可能来自控制面，须限制在本机 int 可安全处理的数值范围内。
	for _, prov := range cfg.Providers {
		if prov.NativeMultimodalEmbeddingsURL != "" {
			u, err := url.Parse(prov.NativeMultimodalEmbeddingsURL)
			if prov.Adapter != "qwen" || err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Fragment != "" {
				return TopologyStats{}, fmt.Errorf("拓扑替换被拒绝: provider %q native_multimodal_embeddings_url 非法或适配器不支持", prov.Name)
			}
		}
		if prov.Weight <= 0 || int64(prov.Weight) > config.MaxTopologyInt {
			return TopologyStats{}, fmt.Errorf(
				"拓扑替换被拒绝: provider %q weight 必须 >0 且 ≤%d, 实际 %d", prov.Name, config.MaxTopologyInt, prov.Weight)
		}
		if prov.Priority < 0 || int64(prov.Priority) > config.MaxTopologyInt {
			return TopologyStats{}, fmt.Errorf(
				"拓扑替换被拒绝: provider %q priority 必须在 [0,%d], 实际 %d", prov.Name, config.MaxTopologyInt, prov.Priority)
		}
	}
	for alias, cands := range cfg.Routes {
		for _, c := range cands {
			if c.Priority < 0 || int64(c.Priority) > config.MaxTopologyInt {
				return TopologyStats{}, fmt.Errorf(
					"拓扑替换被拒绝: model_routes[%q] priority 必须在 [0,%d], 实际 %d", alias, config.MaxTopologyInt, c.Priority)
			}
			if int64(c.Weight) > config.MaxTopologyInt {
				return TopologyStats{}, fmt.Errorf(
					"拓扑替换被拒绝: model_routes[%q] weight 超出数值域(≤%d)", alias, config.MaxTopologyInt)
			}
		}
	}
	known := map[string]bool{}
	for _, prov := range cfg.Providers {
		known[prov.Name] = true
	}
	if missing := checkPoolCoverage(cfg.Routes, known); len(missing) > 0 {
		return TopologyStats{}, fmt.Errorf("拓扑替换被拒绝: 候选引用了未定义 provider %v", missing)
	}

	stats := TopologyStats{}
	oldByName := providerByName(old)
	pools := make(map[string]*keypool.Pool, len(cfg.Providers))
	adapters := make(map[string]adapter.Adapter, len(cfg.Providers))
	for _, prov := range cfg.Providers {
		if prev, ok := oldByName[prov.Name]; ok {
			delete(oldByName, prov.Name)
			if providersEqual(prev, prov) {
				pools[prov.Name] = old.pools[prov.Name] // 运行态无损保留
				adapters[prov.Name] = old.adapters[prov.Name]
				stats.Reused++
				continue
			}
		}
		pool, ad, err := buildPool(prov)
		if err != nil {
			return TopologyStats{}, fmt.Errorf("拓扑替换被拒绝: provider %q 构建失败: %w", prov.Name, err)
		}
		pools[prov.Name] = pool
		adapters[prov.Name] = ad
		stats.Created++
	}
	stats.Removed = len(oldByName) // 旧有新无：池与适配器随旧快照释放，在途请求持旧快照完成

	p.topo.Store(&topologySnapshot{
		providers:   append([]config.Provider(nil), cfg.Providers...),
		routes:      sortRoutes(cfg.Routes),
		pools:       pools,
		adapters:    adapters,
		caps:        buildCaps(cfg.Capabilities),
		nativeResps: buildNativeResps(cfg.Providers),
		loadedAt:    time.Now(),
	})
	return stats, nil
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

// providersEqual 比较 provider 的拓扑配置，包括 key 引用列表。
func providersEqual(a, b config.Provider) bool {
	if a.Name != b.Name || a.Adapter != b.Adapter || a.BaseURL != b.BaseURL ||
		a.NativeMultimodalEmbeddingsURL != b.NativeMultimodalEmbeddingsURL ||
		a.Weight != b.Weight || a.Priority != b.Priority || len(a.Keys) != len(b.Keys) {
		return false
	}
	for i := range a.Keys {
		if a.Keys[i] != b.Keys[i] {
			return false
		}
	}
	return true
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
