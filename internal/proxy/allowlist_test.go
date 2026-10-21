// key 级模型白名单行为：白名单外 403 且上游零调用、白名单内放行、空白名单恒放行、
// 白名单外不吃语义缓存命中、插件改写 model 后再校验、六 lane 同规、
// 模型发现列表过滤与单查 403/404 分支。
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
	"github.com/solvela/muniu/internal/auth"
	"github.com/solvela/muniu/internal/config"
	"github.com/solvela/muniu/internal/keypool"
	"github.com/solvela/muniu/internal/plugin"
	"github.com/solvela/muniu/internal/semantic"
	"github.com/solvela/muniu/internal/testkit"
)

// newAllowProxy 组装带白名单注入的网关：两别名同指同一假上游，KeyInfo.AllowedModels
// 由包装层挂入请求 ctx，与生产路径鉴权中间件的挂载语义一致，六 lane 与模型发现同挂。
func newAllowProxy(t *testing.T, up *testkit.Upstream, allow []string, reg *plugin.Registry) *httptest.Server {
	t.Helper()
	adreg := adapter.NewRegistry()
	adreg.Register("fake", adapter.NewOpenAI(up.URL(), "sk-fake"))
	cfg := Config{
		Registry: adreg,
		Routes: map[string][]config.Candidate{
			"alias-a": {{Provider: "fake", Model: "up-a", Priority: 0}},
			"alias-b": {{Provider: "fake", Model: "up-a", Priority: 0}},
		},
		Timeouts: defTimeouts(),
		Pools: map[string]*keypool.Pool{
			"fake": keypool.NewPool([]keypool.KeyInput{{ID: "K", Value: "sk-test", Weight: 1}}),
		},
		RandIntn: func(n int) int { return 0 },
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if reg != nil {
		cfg.Plugins = reg
	}
	p := New(cfg)
	mux := http.NewServeMux()
	mux.Handle("POST /v1/chat/completions", p.Handler())
	mux.Handle("POST /v1/messages", p.HandlerMessages())
	mux.Handle("POST /v1/responses", p.HandlerResponses())
	mux.Handle("POST /v1/embeddings", p.HandlerEmbeddings())
	mux.Handle("POST /v1/images/generations", p.HandlerImages())
	mux.Handle("POST /v1beta/models/{modelMethod}", p.HandlerGemini())
	mux.Handle("GET /v1/models", p.HandlerModels())
	mux.Handle("GET /v1/models/{model}", p.HandlerModels())
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		info := &auth.KeyInfo{ID: "k1", TenantID: "t1", AllowedModels: allow}
		mux.ServeHTTP(w, r.WithContext(auth.WithKeyInfo(r.Context(), info)))
	}))
	t.Cleanup(ts.Close)
	return ts
}

// doPost 通用 lane 请求助手：JSON POST，读全响应体。
func doPost(t *testing.T, baseURL, path, body string) (*http.Response, string) {
	t.Helper()
	resp, err := http.Post(baseURL+path, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp, string(raw)
}

// assertForbidden 断言 403 信封：类型 forbidden、code model_not_allowed、上游零调用。
func assertForbidden(t *testing.T, resp *http.Response, body string, up *testkit.Upstream) {
	t.Helper()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("应 403,实际 %d: %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "forbidden") || !strings.Contains(body, "model_not_allowed") {
		t.Fatalf("403 信封类型/机器码不符: %s", body)
	}
	if got := up.RequestCount(); got != 0 {
		t.Fatalf("白名单外请求不得触达上游,实际 %d 次", got)
	}
}

// chat 主路径：白名单外 403，上游零调用，message 可读。
func TestAllowlist_chat_白名单外403零上游(t *testing.T) {
	up := testkit.StartJSONUpstream(t, 200, `{"id":"x","choices":[]}`)
	ts := newAllowProxy(t, up, []string{"alias-a"}, nil)
	resp, body := postChat(t, ts.URL, `{"model":"alias-b","messages":[{"role":"user","content":"hi"}]}`)
	assertForbidden(t, resp, body, up)
	if !strings.Contains(body, "无权访问") {
		t.Fatalf("message 应可读说明拒绝原因: %s", body)
	}
}

// 白名单内正常放行；空白名单视为不限。
func TestAllowlist_chat_白名单内放行与空白名单恒放行(t *testing.T) {
	up := testkit.StartJSONUpstream(t, 200, `{"id":"x","choices":[]}`)
	ts := newAllowProxy(t, up, []string{"alias-a"}, nil)
	resp, body := postChat(t, ts.URL, `{"model":"alias-a","messages":[{"role":"user","content":"hi"}]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("白名单内应放行 200: %d %s", resp.StatusCode, body)
	}
	if up.RequestCount() != 1 {
		t.Fatalf("白名单内应正常出站: %d", up.RequestCount())
	}
	ts2 := newAllowProxy(t, up, nil, nil)
	resp, body = postChat(t, ts2.URL, `{"model":"alias-b","messages":[{"role":"user","content":"hi"}]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("空白名单应恒放行: %d %s", resp.StatusCode, body)
	}
}

// 白名单外请求即使存在可命中的语义缓存条目也不得回放，校验先于缓存检索。
func TestAllowlist_chat_白名单外不吃语义缓存命中(t *testing.T) {
	up := testkit.StartJSONUpstream(t, 200,
		`{"id":"x","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	c, err := semantic.NewCache(newTestDB(t), semantic.Options{
		Dim: 4, Threshold: 0.5, TTL: time.Hour, Capacity: 100, EmbeddingModel: "emb",
	}, constEmbedder{})
	if err != nil {
		t.Fatal(err)
	}
	adreg := adapter.NewRegistry()
	adreg.Register("fake", adapter.NewOpenAI(up.URL(), "sk-fake"))
	p := New(Config{
		Registry: adreg,
		Routes: map[string][]config.Candidate{
			"alias-a": {{Provider: "fake", Model: "up-a", Priority: 0}},
			"alias-b": {{Provider: "fake", Model: "up-a", Priority: 0}},
		},
		Cache:        c,
		TenantSwitch: func(string) bool { return true },
		Timeouts:     defTimeouts(),
		Pools: map[string]*keypool.Pool{
			"fake": keypool.NewPool([]keypool.KeyInput{{ID: "K", Value: "sk-test", Weight: 1}}),
		},
		RandIntn: func(n int) int { return 0 },
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	mux := http.NewServeMux()
	mux.Handle("POST /v1/chat/completions", p.Handler())
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// TenantID 须为播种租户，作为 semantic_cache 外键；开关恒真使缓存通路参与。
		info := &auth.KeyInfo{ID: "k1", TenantID: testTenant, AllowedModels: []string{"alias-a"}}
		mux.ServeHTTP(w, r.WithContext(auth.WithKeyInfo(r.Context(), info)))
	}))
	t.Cleanup(ts.Close)

	// 先证缓存通路工作：第一次 miss 出站入库，第二次命中回放，上游仍只 1 次。
	// temperature 显式 ≤0.3 是缓存准入条件，缺省请求恒 skip。
	for i := 0; i < 2; i++ {
		resp, body := postChat(t, ts.URL, `{"model":"alias-a","temperature":0.1,"messages":[{"role":"user","content":"hi"}]}`)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("白名单内请求应 200: %d %s", resp.StatusCode, body)
		}
	}
	if got := up.RequestCount(); got != 1 {
		t.Fatalf("第二次应吃语义缓存命中,上游实收 %d 次", got)
	}
	// 白名单外：可命中缓存存在仍 403，零上游；基线比较，命中请求已让上游计数为 1。
	baseline := up.RequestCount()
	resp, body := postChat(t, ts.URL, `{"model":"alias-b","temperature":0.1,"messages":[{"role":"user","content":"hi"}]}`)
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(body, "model_not_allowed") {
		t.Fatalf("白名单外应 403 model_not_allowed: %d %s", resp.StatusCode, body)
	}
	if got := up.RequestCount(); got != baseline {
		t.Fatalf("被禁请求不得触达上游: 基线 %d,实际 %d", baseline, got)
	}
}

// 插件把白名单内的 model 改写为白名单外别名：重解析后再校验必须拦截，改写不得绕过白名单。
func TestAllowlist_插件改写model后再校验(t *testing.T) {
	up := testkit.StartJSONUpstream(t, 200, `{"id":"x","choices":[]}`)
	reg := manualRegistry(t, "model-rewriter", testkit.FixtureModifyModel, 200)
	ts := newAllowProxy(t, up, []string{"alias-a"}, reg)
	// 请求本身是白名单内的 alias-a，插件把请求体改成含 alias-b 的合法 chat 体。
	resp, body := postChat(t, ts.URL, `{"model":"alias-a","messages":[{"role":"user","content":"hi"}]}`)
	assertForbidden(t, resp, body, up)
}

// 六 lane 同规：messages/responses/gemini/embeddings/images 白名单外一律同形 403、零上游，
// chat 主路径另有专项场景。
func TestAllowlist_多lane同规(t *testing.T) {
	up := testkit.StartJSONUpstream(t, 200, `{"id":"x","choices":[]}`)
	ts := newAllowProxy(t, up, []string{"alias-a"}, nil)
	cases := []struct {
		name string
		path string
		body string
	}{
		{"messages", "/v1/messages", `{"model":"alias-b","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`},
		{"responses", "/v1/responses", `{"model":"alias-b","input":"hi"}`},
		{"gemini", "/v1beta/models/alias-b:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`},
		{"embeddings", "/v1/embeddings", `{"model":"alias-b","input":"hi"}`},
		{"images", "/v1/images/generations", `{"model":"alias-b","prompt":"a cat","n":1}`},
	}
	for _, tc := range cases {
		// gemini lane 返回原生信封形态：管道错误经 writer 改写为 Gemini 形态，不携带 muniu code，
		// 故只断言 403 加访问拒绝语义，不强求 model_not_allowed 字面量。
		resp, body := doPost(t, ts.URL, tc.path, tc.body)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("[%s] 应 403,实际 %d: %s", tc.name, resp.StatusCode, body)
		}
		if tc.name != "gemini" && !strings.Contains(body, "model_not_allowed") {
			t.Fatalf("[%s] 信封应含 model_not_allowed: %s", tc.name, body)
		}
	}
	if got := up.RequestCount(); got != 0 {
		t.Fatalf("六 lane 白名单外均不得触达上游,实际 %d 次", got)
	}
}

// messages 流式白名单外请求返回 Anthropic permission_error，且不触达上游。
func TestAllowlist_messages流式403信封类型(t *testing.T) {
	up := testkit.StartJSONUpstream(t, 200, `{"id":"x","choices":[]}`)
	ts := newAllowProxy(t, up, []string{"alias-a"}, nil)
	resp, body := doPost(t, ts.URL, "/v1/messages",
		`{"model":"alias-b","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("流式 messages 白名单外应 403,实际 %d: %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "permission_error") {
		t.Fatalf("信封类型应映射为 Anthropic permission_error: %s", body)
	}
	if strings.Contains(body, "api_error") {
		t.Fatalf("不得落回 api_error 兜底: %s", body)
	}
	if got := up.RequestCount(); got != 0 {
		t.Fatalf("白名单外流式请求不得触达上游,实际 %d 次", got)
	}
}

// 模型发现：列表按 key 白名单过滤；单查白名单内 200、在目录但白名单外 403、不在目录 404。
func TestAllowlist_模型发现过滤与单查分支(t *testing.T) {
	up := testkit.StartJSONUpstream(t, 200, `{"id":"x","choices":[]}`)
	ts := newAllowProxy(t, up, []string{"alias-a"}, nil)

	resp, body := doGet(t, ts.URL, "/v1/models")
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "alias-a") || strings.Contains(body, "alias-b") {
		t.Fatalf("列表应为白名单过滤视图: %d %s", resp.StatusCode, body)
	}
	resp, body = doGet(t, ts.URL, "/v1/models/alias-a")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("白名单内单查应 200: %d %s", resp.StatusCode, body)
	}
	resp, body = doGet(t, ts.URL, "/v1/models/alias-b")
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(body, "model_not_allowed") {
		t.Fatalf("在目录但白名单外应 403 model_not_allowed: %d %s", resp.StatusCode, body)
	}
	resp, body = doGet(t, ts.URL, "/v1/models/nope")
	if resp.StatusCode != http.StatusNotFound || !strings.Contains(body, "not_found") {
		t.Fatalf("不在目录应 404 not_found: %d %s", resp.StatusCode, body)
	}
}

func doGet(t *testing.T, baseURL, path string) (*http.Response, string) {
	t.Helper()
	resp, err := http.Get(baseURL + path)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp, string(raw)
}
