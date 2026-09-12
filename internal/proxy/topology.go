// 拓扑快照与热替换管道(ADR-0005 配置源抽象 3.1):providers/model_routes/能力目录
// 构成不可变快照,atomic.Pointer 原子替换;转发路径每请求入口加载一次(请求全程
// 同一快照——换道候选视图一致),零锁开销。替换语义:基础校验失败整体拒绝(保持旧
// 快照);provider 配置未变(DeepEqual)则 key 池原样保留——冷却/EMA 等运行态无损。
package proxy

import (
	"errors"
	"fmt"

	"github.com/solvela/muniu/internal/adapter"
	"github.com/solvela/muniu/internal/config"
	"github.com/solvela/muniu/internal/keypool"
)

// topologySnapshot 不可变拓扑:四要素同生共死,替换时整体换新。
// adapter 实例随快照走——热替换失败路径的回滚天然一致,杜绝「旧池+新适配器」
// 的混合转发状态(buildPool 的注册副作用被快照化吸收)。
type topologySnapshot struct {
	providers []config.Provider             // 原始定义(池保留的 DeepEqual 基准)
	routes    map[string][]config.Candidate // priority 升序已排序
	pools     map[string]*keypool.Pool
	adapters  map[string]adapter.Adapter // provider → 出站适配器(与池同生命周期)
	caps      map[governKey]config.ModelCapability
}

// TopologyStats 一次替换的池处置统计(可观测/日志)。
type TopologyStats struct {
	Reused  int // 配置未变,运行态无损保留
	Created int // 新增或配置变更,池重建
	Removed int // 拓扑中消失,池丢弃(在途请求持旧池完成)
}

// Topology 当前快照的只读视图(管理面/诊断:供应商健康页与热替换同步)。
func (p *Proxy) Topology() ([]config.Provider, map[string]*keypool.Pool) {
	s := p.topo.Load()
	if s == nil {
		return nil, nil
	}
	return s.providers, s.pools
}

// ReplaceTopology 拓扑热替换管道:轻校验 → 池保留决策 → 原子替换。
// cfg 携带新拓扑(Providers/Routes/Capabilities 必备,其余字段沿用现值);
// buildPool 由装配层提供(环境变量解析 + adapter 构造 + keypool.NewPool),
// 仅对「新增或配置变更」的 provider 调用——配置未变的池与适配器原样保留,
// 运行态无损。校验/构建失败返回错误且旧快照原封不动(3.3 损坏拒绝应用守门)。
func (p *Proxy) ReplaceTopology(cfg Config,
	buildPool func(config.Provider) (*keypool.Pool, adapter.Adapter, error)) (TopologyStats, error) {
	if len(cfg.Routes) == 0 {
		return TopologyStats{}, errors.New("拓扑替换被拒绝: model_routes 为空(下线全部路由不合法)")
	}
	if buildPool == nil {
		return TopologyStats{}, errors.New("拓扑替换被拒绝: 池构建回调缺失")
	}
	old := p.topo.Load()
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
	stats.Removed = len(oldByName) // 旧有新无:池与适配器随旧快照释放(在途请求持旧快照完成)

	p.topo.Store(&topologySnapshot{
		providers: append([]config.Provider(nil), cfg.Providers...),
		routes:    sortRoutes(cfg.Routes),
		pools:     pools,
		adapters:  adapters,
		caps:      buildCaps(cfg.Capabilities),
	})
	return stats, nil
}

// checkPoolCoverage 路由候选引用的全部 provider 必须在拓扑内定义(返回缺失名单)。
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

// providersEqual 配置级恒等(含 key 引用列表);环境变量值的变更不属拓扑范畴
// (值经 .env 注入,池内 key 值启动期已解析)。
func providersEqual(a, b config.Provider) bool {
	if a.Name != b.Name || a.Adapter != b.Adapter || a.BaseURL != b.BaseURL ||
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

// sortRoutes 候选按 priority 升序(与 New 同规则,替换路径复用)。
func sortRoutes(routes map[string][]config.Candidate) map[string][]config.Candidate {
	sorted := make(map[string][]config.Candidate, len(routes))
	for alias, cands := range routes {
		cs := append([]config.Candidate(nil), cands...)
		for i := 1; i < len(cs); i++ { // 插入排序:候选列表极短
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
