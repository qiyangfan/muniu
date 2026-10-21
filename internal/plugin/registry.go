// 插件注册与生命周期：按名注册与替换、启停、顺序快照执行请求体钩子，版本由
// SHA-256 内容哈希固定。替换热生效：新请求走新版本，在途请求在旧版本上完成；
// 禁用插件不进快照，主路径零开销。
package plugin

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/solvela/muniu/internal/metrics"
)

// Info 插件元信息视图，管理面列表消费。
type Info struct {
	Name     string `json:"name"`
	SHA256   string `json:"sha256"`
	Enabled  bool   `json:"enabled"`
	FailOpen bool   `json:"fail_open"`
	ByteLen  int    `json:"binary_bytes"`
}

// entry 单插件登记项。
type entry struct {
	p       *Plugin
	sha256  string
	enabled atomic.Bool
}

// Registry 插件注册表。
type Registry struct {
	mu    sync.RWMutex
	m     map[string]*entry
	order []string // 注册序，ApplyToBody 按此顺序执行，稳定可复现。
	gauge *metrics.GaugeFunc

	// 热替换与关停的在途守护按插件持有者计数，见 Plugin.snapHolders：只等持有
	// 旧版本的快照归零，持续流量下的新快照不会无限推迟关闭。
}

// NewRegistry 构造空注册表。
func NewRegistry() *Registry {
	r := &Registry{m: map[string]*entry{}}
	r.gauge = metrics.NewGaugeFunc("muniu_plugin_instances", "插件 wasm 实例数(按状态)", r.instanceSamples)
	return r
}

// acquireSnapshot 取启用插件快照并登记持有者，必须与 releaseSnapshot 配对调用。
func (r *Registry) acquireSnapshot() []*Plugin {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Plugin, 0, len(r.order))
	for _, name := range r.order {
		if e, ok := r.m[name]; ok && e.enabled.Load() {
			out = append(out, e.p)
			e.p.holdSnapshot()
		}
	}
	return out
}

func (r *Registry) releaseSnapshot(snapshot []*Plugin) {
	for _, p := range snapshot {
		p.releaseSnapshotSlot()
	}
}

// drainAndClose 异步等旧版本持有者清零后关闭，不阻塞注册与关停调用方；
// 持有者受 hook 超时约束，清零必然收敛。
func (r *Registry) drainAndClose(p *Plugin) {
	go func() {
		p.snapMu.Lock()
		for p.snapHolders > 0 {
			p.snapCond.Wait()
		}
		p.snapMu.Unlock()
		_ = p.Close()
	}()
}

// instanceSamples 汇总各插件实例水位，指标抓取时回调。
func (r *Registry) instanceSamples() []metrics.Sample {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []metrics.Sample
	for name, e := range r.m {
		live := e.p.pool.createdTotal.Load() - e.p.pool.closedTotal.Load()
		out = append(out,
			metrics.Sample{Labels: [][2]string{{"plugin", name}, {"state", "live"}}, Value: float64(live)},
			metrics.Sample{Labels: [][2]string{{"plugin", name}, {"state", "idle"}}, Value: float64(e.p.pool.idleSnapshot())},
		)
	}
	return out
}

// Register 注册新插件，同名即版本替换：内容哈希固定版本，新请求热生效，旧版本在
// 在途请求完成后关闭；启停状态沿用旧版本，版本替换不等于状态变更。
// 注册即校验：Compile 任一步失败即拒载，既有插件不受影响。
func (r *Registry) Register(opts Options) (Info, bool, error) {
	opts.Name = strings.TrimSpace(opts.Name)
	sum := sha256.Sum256(opts.Binary)
	sumHex := hex.EncodeToString(sum[:])

	p, err := Compile(opts)
	if err != nil {
		return Info{}, false, err
	}
	e := &entry{p: p, sha256: sumHex}

	r.mu.Lock()
	old, replaced := r.m[opts.Name]
	if replaced {
		e.enabled.Store(old.enabled.Load())
	} else {
		e.enabled.Store(opts.Enabled)
		r.order = append(r.order, opts.Name)
	}
	r.m[opts.Name] = e
	r.mu.Unlock()

	if replaced { // 锁外关停旧版本，先等在途快照跑完，异步不阻塞注册表与请求路径。
		r.drainAndClose(old.p)
	}
	return Info{
		Name:     opts.Name,
		SHA256:   sumHex,
		Enabled:  e.enabled.Load(),
		FailOpen: opts.FailOpen,
		ByteLen:  len(opts.Binary),
	}, replaced, nil
}

// Enable 启停，停用后不进执行快照，主路径零开销。
// 需写锁与 Register 的替换路径线性化：Register 锁内搬运旧 entry 的启停状态，
// 否则启停可能改在已被遗弃的旧 entry 上而丢失更新。
func (r *Registry) Enable(name string, enabled bool) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.m[strings.TrimSpace(name)]
	if !ok {
		return false
	}
	e.enabled.Store(enabled)
	return true
}

// Enabled 查询启停状态。
func (r *Registry) Enabled(name string) bool {
	r.mu.RLock()
	e, ok := r.m[strings.TrimSpace(name)]
	r.mu.RUnlock()
	return ok && e.enabled.Load()
}

// List 全量登记视图，含停用。
func (r *Registry) List() []Info {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Info, 0, len(r.order))
	for _, name := range r.order {
		e, ok := r.m[name]
		if !ok {
			continue
		}
		out = append(out, Info{
			Name:     name,
			SHA256:   e.sha256,
			Enabled:  e.enabled.Load(),
			FailOpen: e.p.opts.FailOpen,
			ByteLen:  len(e.p.opts.Binary),
		})
	}
	return out
}

// SHA256 当前版本内容哈希，管理面替换幂等判据。
func (r *Registry) SHA256(name string) (string, bool) {
	r.mu.RLock()
	e, ok := r.m[strings.TrimSpace(name)]
	r.mu.RUnlock()
	if !ok {
		return "", false
	}
	return e.sha256, true
}

// Close 全部下线，进程退出路径：先等在途快照跑完，再逐个关闭。
func (r *Registry) Close() {
	r.mu.Lock()
	entries := make([]*entry, 0, len(r.m))
	for _, e := range r.m {
		entries = append(entries, e)
	}
	r.m = map[string]*entry{}
	r.order = nil
	r.mu.Unlock()

	for _, e := range entries {
		e.p.snapMu.Lock() // 等本插件的在途快照清零，同 drainAndClose 的同步版。
		for e.p.snapHolders > 0 {
			e.p.snapCond.Wait()
		}
		e.p.snapMu.Unlock()
		_ = e.p.Close()
	}
}
