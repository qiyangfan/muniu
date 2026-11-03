package proxy

import (
	"errors"
	"io"
	"log/slog"
	"math"
	"testing"

	"github.com/solvela/muniu/internal/adapter"
	"github.com/solvela/muniu/internal/config"
	"github.com/solvela/muniu/internal/keypool"
	"github.com/solvela/muniu/internal/testkit"
)

// 拓扑热替换管道测试：池保留语义、校验拒绝、替换生效、构建失败回滚。

func topoConfig(name, baseURL string) Config {
	prov := config.Provider{
		Name: name, Adapter: "openai", BaseURL: baseURL, Weight: 1,
		Keys: []config.KeyRef{{Env: name + "_KEY", Weight: 1}},
	}
	pool := keypool.NewPool([]keypool.KeyInput{{ID: name + "-K1", Value: "sk-" + name, Weight: 1}})
	return Config{
		Providers: []config.Provider{prov},
		Routes: map[string][]config.Candidate{
			"m1": {{Provider: name, Model: "model-x", Priority: 1, Weight: 1}},
		},
		Pools: map[string]*keypool.Pool{name: pool},
	}
}

func buildOK(prov config.Provider) (*keypool.Pool, adapter.Adapter, error) {
	pool := keypool.NewPool([]keypool.KeyInput{
		{ID: prov.Name + "-K2", Value: "sk-new-" + prov.Name, Weight: 1},
	})
	return pool, adapter.NewOpenAI("http://"+prov.Name+".test", ""), nil
}

func TestTopology_InitialSnapshotFromNew(t *testing.T) {
	cfg := topoConfig("alpha", "http://alpha")
	p := New(cfg)
	providers, pools := p.Topology()
	if len(providers) != 1 || providers[0].Name != "alpha" {
		t.Fatalf("初始快照 providers 异常: %v", providers)
	}
	if pools["alpha"] == nil {
		t.Fatal("初始快照缺 alpha 池")
	}
	if _, ok := p.topo.Load().routes["m1"]; !ok {
		t.Fatal("初始快照缺 m1 路由")
	}
}

func TestTopology_原生向量URL变更触发原子替换(t *testing.T) {
	initial := topoConfig("alpha", "http://alpha")
	initial.Providers[0].Adapter = "qwen"
	initial.Providers[0].NativeMultimodalEmbeddingsURL = "https://alpha/native-v1"
	p := New(initial)
	oldPool := p.topo.Load().pools["alpha"]

	next := topoConfig("alpha", "http://alpha")
	next.Providers[0].Adapter = "qwen"
	next.Providers[0].NativeMultimodalEmbeddingsURL = "https://alpha/native-v2"
	called := 0
	stats, err := p.ReplaceTopology(next, func(prov config.Provider) (*keypool.Pool, adapter.Adapter, error) {
		called++
		if prov.NativeMultimodalEmbeddingsURL != "https://alpha/native-v2" {
			t.Fatalf("构建回调未收到新 URL: %+v", prov)
		}
		return keypool.NewPool([]keypool.KeyInput{{ID: "new", Value: "new", Weight: 1}}), adapter.NewOpenAI(prov.BaseURL, ""), nil
	})
	if err != nil || called != 1 || stats.Created != 1 || stats.Reused != 0 {
		t.Fatalf("URL 变更应重建 provider 快照: stats=%+v called=%d err=%v", stats, called, err)
	}
	providers, pools := p.Topology()
	if providers[0].NativeMultimodalEmbeddingsURL != "https://alpha/native-v2" || pools["alpha"] == oldPool {
		t.Fatalf("新快照未原子生效: provider=%+v", providers[0])
	}
}

func TestTopology_Replace_CreatedAndReplaced(t *testing.T) {
	cfg := topoConfig("alpha", "http://alpha")
	p := New(cfg)
	oldPool := func() *keypool.Pool { s := p.topo.Load(); return s.pools["alpha"] }()

	// 替换：alpha 换 base_url，配置变更导致池与适配器重建，并新增 beta。
	next := topoConfig("alpha", "http://alpha-v2")
	beta := topoConfig("beta", "http://beta")
	next.Providers = append(next.Providers, beta.Providers...)
	next.Routes["m2"] = []config.Candidate{{Provider: "beta", Model: "model-y", Priority: 1}}

	stats, err := p.ReplaceTopology(next, buildOK)
	if err != nil {
		t.Fatalf("替换失败: %v", err)
	}
	if stats.Created != 2 || stats.Reused != 0 || stats.Removed != 0 {
		t.Fatalf("统计异常: %+v", stats)
	}
	s := p.topo.Load()
	if s.pools["alpha"] == oldPool {
		t.Fatal("配置变更的 provider 池应重建,却复用了旧池")
	}
	if _, ok := s.routes["m2"]; !ok {
		t.Fatal("新路由未生效")
	}
}

func TestTopology_Replace_UnchangedProviderKeepsRuntimePool(t *testing.T) {
	cfg := topoConfig("alpha", "http://alpha")
	p := New(cfg)
	oldPool := func() *keypool.Pool { s := p.topo.Load(); return s.pools["alpha"] }()

	// alpha 配置逐字未变，仅追加一个新路由：池必须复用，冷却与 EMA 运行态无损。
	next := topoConfig("alpha", "http://alpha")
	next.Routes["m2"] = []config.Candidate{{Provider: "alpha", Model: "model-y", Priority: 2}}
	stats, err := p.ReplaceTopology(next, buildOK)
	if err != nil {
		t.Fatalf("替换失败: %v", err)
	}
	if stats.Reused != 1 {
		t.Fatalf("未变 provider 应复用池,统计: %+v", stats)
	}
	if s := p.topo.Load(); s.pools["alpha"] != oldPool {
		t.Fatal("配置未变的 provider 池指针应保持(运行态无损)")
	}
}

func TestTopology_Replace_RemovedProvider(t *testing.T) {
	cfg := topoConfig("alpha", "http://alpha")
	beta := topoConfig("beta", "http://beta")
	cfg.Providers = append(cfg.Providers, beta.Providers...)
	cfg.Routes["m2"] = []config.Candidate{{Provider: "beta", Model: "model-y", Priority: 1}}
	p := New(cfg)

	stats, err := p.ReplaceTopology(topoConfig("alpha", "http://alpha"), buildOK)
	if err != nil {
		t.Fatalf("替换失败: %v", err)
	}
	if stats.Removed != 1 {
		t.Fatalf("消失的 provider 应计入 Removed: %+v", stats)
	}
	if _, ok := p.topo.Load().routes["m2"]; ok {
		t.Fatal("被移除 provider 的路由应不可达")
	}
}

func TestTopology_Replace_RejectInvalid(t *testing.T) {
	cfg := topoConfig("alpha", "http://alpha")
	p := New(cfg)

	// 空路由拒绝
	if _, err := p.ReplaceTopology(Config{}, buildOK); err == nil {
		t.Fatal("空路由应被拒绝")
	}
	// 候选引用未定义 provider 拒绝
	bad := topoConfig("alpha", "http://alpha")
	bad.Routes["m9"] = []config.Candidate{{Provider: "ghost", Model: "m", Priority: 1}}
	if _, err := p.ReplaceTopology(bad, buildOK); err == nil {
		t.Fatal("候选引用未定义 provider 应被拒绝")
	}
	// 池构建失败拒绝：buildPool 报错则整体回滚。
	failing := topoConfig("alpha", "http://alpha-v3")
	beta := topoConfig("beta", "http://beta")
	failing.Providers = append(failing.Providers, beta.Providers...)
	failing.Routes["m2"] = []config.Candidate{{Provider: "beta", Model: "model-y", Priority: 1}}
	if _, err := p.ReplaceTopology(failing, func(prov config.Provider) (*keypool.Pool, adapter.Adapter, error) {
		if prov.Name == "beta" {
			return nil, nil, errors.New("env 缺失")
		}
		return buildOK(prov)
	}); err == nil {
		t.Fatal("池构建失败应整体拒绝替换")
	}
	// 上述拒绝后旧快照原封不动，含 alpha 运行态。
	s := p.topo.Load()
	if _, ok := s.routes["m1"]; !ok || s.pools["alpha"] == nil {
		t.Fatal("拒绝替换后旧快照必须保持")
	}
}

// TestTopology_Replace_RejectsOutOfIntDomain 数值域守门：weight/priority 超出数据面 int 域时
// 必须整体拒绝且旧快照保持，failover 加权累加为 int 运算，超域巨值溢出为负会使加权随机失效。
func TestTopology_Replace_RejectsOutOfIntDomain(t *testing.T) {
	cfg := topoConfig("alpha", "http://alpha")
	p := New(cfg)

	huge := int(math.MaxInt32) + 1
	next := topoConfig("alpha", "http://alpha-v2")
	next.Providers[0].Weight = huge // provider weight 超域
	if _, err := p.ReplaceTopology(next, buildOK); err == nil {
		t.Fatal("provider weight 超 int 域应拒绝替换")
	}

	next2 := topoConfig("alpha", "http://alpha-v2")
	next2.Providers[0].Priority = -1 // priority 负值与 YAML validate 同规拒绝
	if _, err := p.ReplaceTopology(next2, buildOK); err == nil {
		t.Fatal("provider priority 负值应拒绝替换")
	}

	next3 := topoConfig("alpha", "http://alpha-v2")
	next3.Routes["m1"] = []config.Candidate{{Provider: "alpha", Model: "model-x", Priority: 1, Weight: huge}}
	if _, err := p.ReplaceTopology(next3, buildOK); err == nil {
		t.Fatal("candidate weight 超 int 域应拒绝替换")
	}

	// 三次拒绝后旧快照原封不动
	s := p.topo.Load()
	if s.pools["alpha"] == nil || s.providers[0].BaseURL != "http://alpha" {
		t.Fatal("拒绝替换后旧快照必须保持")
	}
}

// TestReplaceTopology_控制面载荷沿用代理声明 评审 R1/R6:控制面拓扑载荷不含 proxy_url
// 字段,InheritProxy=true 时入站空声明沿用旧值并物化进快照(数据面/探针面同源);
// 配置文件源(InheritProxy=false)显式清空即时生效。client 迁移:URL 同→同实例。
func TestReplaceTopology_控制面载荷沿用代理声明(t *testing.T) {
	upA := testkit.StartJSONUpstream(t, 200, "{}")
	rp := newRecordingProxy(t)
	reg := adapter.NewRegistry()
	reg.Register("pa", adapter.NewOpenAI(upA.URL(), "sk-a"))

	newProxyWith := func(providers []config.Provider) *Proxy {
		p := New(Config{
			Registry:  reg,
			Providers: providers,
			Routes:    map[string][]config.Candidate{"alias-a": {{Provider: "pa", Model: "m", Priority: 0}}},
			Timeouts:  defTimeouts(),
			Pools: map[string]*keypool.Pool{
				"pa": keypool.NewPool([]keypool.KeyInput{{ID: "KA", Value: "sk-a", Weight: 1}}),
			},
			Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		})
		return p
	}
	replace := func(p *Proxy, providers []config.Provider, inherit bool) {
		if _, err := p.ReplaceTopology(Config{
			Providers:    providers,
			Routes:       map[string][]config.Candidate{"alias-a": {{Provider: "pa", Model: "m", Priority: 0}}},
			InheritProxy: inherit,
		}, func(prov config.Provider) (*keypool.Pool, adapter.Adapter, error) {
			ad, _ := reg.Get(prov.Name)
			return keypool.NewPool([]keypool.KeyInput{{ID: "KA", Value: "sk-a", Weight: 1}}), ad, nil
		}); err != nil {
			t.Fatalf("替换被拒: %v", err)
		}
	}

	// 控制面源:首次同步载荷不含 proxy_url → 沿用旧声明并物化,client 同实例
	p := newProxyWith([]config.Provider{{Name: "pa", Adapter: "openai", BaseURL: upA.URL(), Weight: 1, ProxyURL: rp.URL}})
	before := p.clientFor(p.topo.Load(), "pa")
	replace(p, []config.Provider{{Name: "pa", Adapter: "openai", BaseURL: upA.URL(), Weight: 1}}, true)
	snap := p.topo.Load()
	if snap.providers[0].ProxyURL != rp.URL {
		t.Fatalf("控制面沿用应物化进快照: %q", snap.providers[0].ProxyURL)
	}
	if after := p.clientFor(snap, "pa"); after != before {
		t.Fatal("沿用分支 client 必须同实例复用")
	}

	// 配置文件源(SIGHUP):显式清空 proxy_url → 即时生效回默认 client(评审 R1 语义)
	p2 := newProxyWith([]config.Provider{{Name: "pa", Adapter: "openai", BaseURL: upA.URL(), Weight: 1, ProxyURL: rp.URL}})
	replace(p2, []config.Provider{{Name: "pa", Adapter: "openai", BaseURL: upA.URL(), Weight: 1}}, false)
	snap2 := p2.topo.Load()
	if snap2.providers[0].ProxyURL != "" {
		t.Fatalf("配置源显式清空不得被沿用吞掉: %q", snap2.providers[0].ProxyURL)
	}
	if _, has := snap2.clients["pa"]; has {
		t.Fatal("清空后不得残留代理 client")
	}
}
