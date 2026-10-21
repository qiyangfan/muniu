// 插件请求体钩子集成测试：拒绝/改写/放行三态、改写产物非法拒绝、
// fail_open 放行、禁用态零开销基准探针。
// fixture 复用 testkit 的手写 wat 字节装配器，与 internal/plugin 同源。
package proxy

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/solvela/muniu/internal/adapter"
	"github.com/solvela/muniu/internal/config"
	"github.com/solvela/muniu/internal/keypool"
	"github.com/solvela/muniu/internal/plugin"
	"github.com/solvela/muniu/internal/testkit"
)

// newPluginTestProxy 组装带插件注册表的代理。
func newPluginTestProxy(t *testing.T, upstreamURL string, reg *plugin.Registry) *httptest.Server {
	t.Helper()
	adreg := adapter.NewRegistry()
	adreg.Register("fake", adapter.NewOpenAI(upstreamURL, "sk-fake"))
	p := New(Config{
		Registry: adreg,
		Routes: map[string][]config.Candidate{
			"alias-a": {{Provider: "fake", Model: "upstream-a", Priority: 0}},
		},
		Timeouts: defTimeouts(),
		Pools: map[string]*keypool.Pool{
			"fake": keypool.NewPool([]keypool.KeyInput{{ID: "K", Value: "sk-test", Weight: 1}}),
		},
		Plugins: reg,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	// 与生产装配同构：chat 与 embeddings 双端点，插件钩子两通道都有挂点。
	mux := http.NewServeMux()
	mux.Handle("POST /v1/chat/completions", p.Handler())
	mux.Handle("POST /v1/embeddings", p.HandlerEmbeddings())
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

// manualRegistry 绕开 metrics 全局注册，Register 走 NewRegistry，测试内轻量构造。
func manualRegistry(t *testing.T, name string, kind testkit.PluginFixtureKind, timeoutMs int64) *plugin.Registry {
	t.Helper()
	reg := plugin.NewRegistry()
	if _, _, err := reg.Register(plugin.Options{
		Name: name, Binary: testkit.FixturePlugin(kind), Enabled: true,
		TimeoutMs: timeoutMs, MemoryLimitPages: 1, PoolSize: 2,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}); err != nil {
		t.Fatalf("注册插件: %v", err)
	}
	t.Cleanup(reg.Close)
	return reg
}

func TestPluginHook_拒绝请求_不进上游(t *testing.T) {
	up := testkit.StartJSONUpstream(t, 200, `{"id":"x"}`) // 上游若被请求则返回成功，便于反证
	reg := manualRegistry(t, "guard", testkit.FixtureReject, 200)
	ts := newPluginTestProxy(t, up.URL(), reg)

	resp, body := postChat(t, ts.URL, chatBody)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("拒绝应为 400,实际 %d: %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "plugin_rejected") || !strings.Contains(body, "pii detected") {
		t.Fatalf("拒绝信封应带插件理由: %s", body)
	}
	if got := up.RequestCount(); got != 0 {
		t.Fatalf("拒绝后不得进入上游候选循环,上游收到 %d 次请求", got)
	}
}

func TestPluginHook_改写留痕_上游收到改写体(t *testing.T) {
	up := testkit.StartJSONUpstream(t, 200, `{"id":"x","choices":[]}`)
	reg := manualRegistry(t, "masker", testkit.FixtureModifyEcho, 200)
	ts := newPluginTestProxy(t, up.URL(), reg)

	resp, _ := postChat(t, ts.URL, chatBody)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("回显改写应放行 200,实际 %d", resp.StatusCode)
	}
	if got := resp.Header.Get("X-Muniu-Plugin"); got != "masker" {
		t.Fatalf("改写留痕头不符: %q", got)
	}
	// 出站体经 adapter.Prepare 规范化重建，messages 前置、model 换为上游模型别名。
	const wantOut = `{"messages":[{"role":"user","content":"hi"}],"model":"upstream-a"}`
	if got := string(up.LastBody()); got != wantOut {
		t.Fatalf("上游应收到的改写后请求体不符:\n got=%q\nwant=%q", got, wantOut)
	}
}

func TestPluginHook_改写产物非法_拒绝(t *testing.T) {
	up := testkit.StartJSONUpstream(t, 200, `{"id":"x"}`)
	reg := manualRegistry(t, "bad-masker", testkit.FixtureModify, 200) // 恒改写为非 JSON 常量
	ts := newPluginTestProxy(t, up.URL(), reg)

	resp, body := postChat(t, ts.URL, chatBody)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("非法改写产物应 400,实际 %d: %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "plugin_rejected") {
		t.Fatalf("拒绝信封 code 不符: %s", body)
	}
	if got := resp.Header.Get("X-Muniu-Plugin"); got != "" {
		t.Fatalf("产物被拒的 400 不得携带留痕头,实际 %q", got)
	}
	if got := up.RequestCount(); got != 0 {
		t.Fatalf("非法产物不得出站,上游收到 %d 次", got)
	}
}

func TestPluginHook_放行_无留痕头(t *testing.T) {
	up := testkit.StartJSONUpstream(t, 200, `{"id":"x","choices":[]}`)
	reg := manualRegistry(t, "passer", testkit.FixturePass, 200)
	ts := newPluginTestProxy(t, up.URL(), reg)

	resp, _ := postChat(t, ts.URL, chatBody)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("放行应 200,实际 %d", resp.StatusCode)
	}
	if got := resp.Header.Get("X-Muniu-Plugin"); got != "" {
		t.Fatalf("未改写不应有留痕头: %q", got)
	}
	const wantOut = `{"messages":[{"role":"user","content":"hi"}],"model":"upstream-a"}`
	if got := string(up.LastBody()); got != wantOut {
		t.Fatalf("上游收到的应为原始请求体(经适配器规范化):\n got=%q\nwant=%q", got, wantOut)
	}
}

func TestPluginHook_fail_closed_containment_拒绝(t *testing.T) {
	up := testkit.StartJSONUpstream(t, 200, `{"id":"x"}`)
	reg := manualRegistry(t, "looper", testkit.FixtureLoop, 80)
	ts := newPluginTestProxy(t, up.URL(), reg)

	start := time.Now()
	resp, body := postChat(t, ts.URL, chatBody)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("fail_closed containment 应 500,实际 %d: %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "plugin_failed") {
		t.Fatalf("信封 code 应为 plugin_failed: %s", body)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("containment 未及时中断: %v", elapsed)
	}
	if got := up.RequestCount(); got != 0 {
		t.Fatalf("containment 后不得出站,上游收到 %d 次", got)
	}
}

func TestPluginHook_fail_open_containment_放行(t *testing.T) {
	up := testkit.StartJSONUpstream(t, 200, `{"id":"x","choices":[]}`)
	reg := plugin.NewRegistry()
	t.Cleanup(reg.Close)
	if _, _, err := reg.Register(plugin.Options{
		Name: "loose", Binary: testkit.FixturePlugin(testkit.FixtureLoop), Enabled: true,
		FailOpen: true, TimeoutMs: 80, MemoryLimitPages: 1, PoolSize: 2,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}); err != nil {
		t.Fatalf("注册: %v", err)
	}
	ts := newPluginTestProxy(t, up.URL(), reg)

	resp, _ := postChat(t, ts.URL, chatBody)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("fail_open 下 containment 应放行原请求 200,实际 %d", resp.StatusCode)
	}
	if got := up.RequestCount(); got != 1 {
		t.Fatalf("fail_open 应继续出站,上游收到 %d 次", got)
	}
}

// BenchmarkPluginHook_禁用态零开销探针：nil registry 与无插件代理逐项对照，
// 断言禁用态不因插件设施引入任何额外分配。
func BenchmarkPluginHook_禁用态零开销(b *testing.B) {
	b.ReportAllocs()
	up := testkit.StartJSONUpstream(b, 200, `{"id":"x","choices":[]}`)
	adreg := adapter.NewRegistry()
	adreg.Register("fake", adapter.NewOpenAI(up.URL(), "sk-fake"))
	p := New(Config{
		Registry: adreg,
		Routes: map[string][]config.Candidate{
			"alias-a": {{Provider: "fake", Model: "upstream-a", Priority: 0}},
		},
		Timeouts: defTimeouts(),
		Pools: map[string]*keypool.Pool{
			"fake": keypool.NewPool([]keypool.KeyInput{{ID: "K", Value: "sk-test", Weight: 1}}),
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ts := httptest.NewServer(p.Handler())
	defer ts.Close()
	req, _ := http.NewRequest("POST", ts.URL+"/v1/chat/completions", strings.NewReader(chatBody))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			b.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
}

// BenchmarkPluginHook_启用态对照：pass 插件启用形态，每请求一次 wasm 调用，与禁用态探针同剧本。
func BenchmarkPluginHook_启用态(b *testing.B) {
	b.ReportAllocs()
	up := testkit.StartJSONUpstream(b, 200, `{"id":"x","choices":[]}`)
	reg := plugin.NewRegistry()
	defer reg.Close()
	if _, _, err := reg.Register(plugin.Options{
		Name: "bench", Binary: testkit.FixturePlugin(testkit.FixturePass), Enabled: true,
		TimeoutMs: 200, MemoryLimitPages: 1, PoolSize: 8,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}); err != nil {
		b.Fatal(err)
	}
	adreg := adapter.NewRegistry()
	adreg.Register("fake", adapter.NewOpenAI(up.URL(), "sk-fake"))
	p := New(Config{
		Registry: adreg,
		Routes: map[string][]config.Candidate{
			"alias-a": {{Provider: "fake", Model: "upstream-a", Priority: 0}},
		},
		Timeouts: defTimeouts(),
		Pools: map[string]*keypool.Pool{
			"fake": keypool.NewPool([]keypool.KeyInput{{ID: "K", Value: "sk-test", Weight: 1}}),
		},
		Plugins: reg,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ts := httptest.NewServer(p.Handler())
	defer ts.Close()
	req, _ := http.NewRequest("POST", ts.URL+"/v1/chat/completions", strings.NewReader(chatBody))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			b.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
}

// ---- embeddings 通道：与 chat 同链序，防回归缺口 ----

const embedBody = `{"model":"alias-a","input":"hello"}`

func postEmbeddings(t *testing.T, url, body string) (*http.Response, string) {
	t.Helper()
	resp, err := http.Post(url+"/v1/embeddings", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读响应失败: %v", err)
	}
	_ = resp.Body.Close()
	return resp, string(b)
}

func TestPluginHook_Embeddings_拒绝(t *testing.T) {
	up := testkit.StartJSONUpstream(t, 200, `{"data":[]}`)
	reg := manualRegistry(t, "guard-e", testkit.FixtureReject, 200)
	ts := newPluginTestProxy(t, up.URL(), reg)

	resp, body := postEmbeddings(t, ts.URL, embedBody)
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "plugin_rejected") {
		t.Fatalf("embeddings 拒绝应 400 plugin_rejected: %d %s", resp.StatusCode, body)
	}
	if got := up.RequestCount(); got != 0 {
		t.Fatalf("拒绝后不得出站,上游收到 %d 次", got)
	}
}

func TestPluginHook_Embeddings_改写留痕(t *testing.T) {
	up := testkit.StartJSONUpstream(t, 200, `{"data":[]}`)
	reg := manualRegistry(t, "masker-e", testkit.FixtureModifyEcho, 200)
	ts := newPluginTestProxy(t, up.URL(), reg)

	resp, _ := postEmbeddings(t, ts.URL, embedBody)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("回显改写应放行 200,实际 %d", resp.StatusCode)
	}
	if got := resp.Header.Get("X-Muniu-Plugin"); got != "masker-e" {
		t.Fatalf("留痕头不符: %q", got)
	}
	if got := up.RequestCount(); got != 1 {
		t.Fatalf("改写后应出站,上游收到 %d 次", got)
	}
}

// BenchmarkPluginHook_空注册表对照：生产恒建空 Registry 形态，对照 nil 禁用态，
// 证实禁用无可测量新增开销对真实生产形态成立。
func BenchmarkPluginHook_空注册表(b *testing.B) {
	b.ReportAllocs()
	up := testkit.StartJSONUpstream(b, 200, `{"id":"x","choices":[]}`)
	adreg := adapter.NewRegistry()
	adreg.Register("fake", adapter.NewOpenAI(up.URL(), "sk-fake"))
	p := New(Config{
		Registry: adreg,
		Routes: map[string][]config.Candidate{
			"alias-a": {{Provider: "fake", Model: "upstream-a", Priority: 0}},
		},
		Timeouts: defTimeouts(),
		Pools: map[string]*keypool.Pool{
			"fake": keypool.NewPool([]keypool.KeyInput{{ID: "K", Value: "sk-test", Weight: 1}}),
		},
		Plugins: plugin.NewRegistry(), // 生产形态：恒建但为空
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ts := httptest.NewServer(p.Handler())
	defer ts.Close()
	req, _ := http.NewRequest("POST", ts.URL+"/v1/chat/completions", strings.NewReader(chatBody))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			b.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
}
