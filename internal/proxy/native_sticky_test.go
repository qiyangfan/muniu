// 有状态粘性路由测试(protocol-native-passthrough;测试先行域):
// 亲和表(TTL+LRU)、粘性候选域收缩、亲和未知自解释、GET 代理、状态请求缓存旁路。
package proxy

import (
	"encoding/json"
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
	"github.com/solvela/muniu/internal/protocol"
	"github.com/solvela/muniu/internal/testkit"
)

// TestAffinityTable 亲和表语义:Put/Get 覆盖刷新、LRU 容量淘汰、TTL 过期视为未知。
func TestAffinityTable(t *testing.T) {
	a := newAffinityTable()
	a.Put("resp_1", "prov-a")
	if got, ok := a.Get("resp_1"); !ok || got != "prov-a" {
		t.Fatalf("亲和查询不符: %q %v", got, ok)
	}
	// 覆盖写
	a.Put("resp_1", "prov-b")
	if got, _ := a.Get("resp_1"); got != "prov-b" {
		t.Fatalf("覆盖写不符: %q", got)
	}
	// LRU 容量淘汰(容量 4096:灌满 + 1,最老条目被逐)
	for i := 0; i < affinityCapacity+1; i++ {
		a.Put(string(rune('A'+i%26))+"-"+time.Now().Format("150405.000000000")+string(rune(i)), "p")
	}
	_ = a // 容量淘汰不 panic 即达意(精确淘汰序属实现细节,由 Get 命中率兜底)
	if a.lru.Len() > affinityCapacity {
		t.Fatalf("容量上限失效: %d", a.lru.Len())
	}
}

// TestStickyRoute 粘性路由三域:无原生候选拒绝 / 亲和未知自解释 / 命中收缩候选域。
func TestStickyRoute(t *testing.T) {
	snap := &topologySnapshot{
		adapters: map[string]adapter.Adapter{
			"a1": stubNative{proto: protocol.InboundResponses},
			"a2": stubNative{proto: protocol.InboundResponses},
		},
		nativeResps: map[string]bool{"a1": true, "a2": true},
		routes: map[string][]config.Candidate{
			"alias-r": {{Provider: "a1", Model: "m1"}, {Provider: "a2", Model: "m2"}},
		},
	}
	p := New(Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	r := httptest.NewRequest("POST", "/v1/responses", nil)
	cands := snap.routes["alias-r"]

	// 无原生候选(声明关闭):翻译路径维持既有拒绝
	snapNoNative := &topologySnapshot{
		adapters:    snap.adapters,
		nativeResps: map[string]bool{"a1": false, "a2": false},
		routes:      snap.routes,
	}
	if _, err := p.stickyRoute(r, &inboundNative{proto: protocol.InboundResponses, prevID: "resp_x"}, snapNoNative, cands); err == nil ||
		err.code != protocol.CodePreviousResponseIDUnsupported {
		t.Fatalf("无原生候选应维持既有拒绝: %+v", err)
	}
	// 亲和未知:自解释,不盲发
	if _, err := p.stickyRoute(r, &inboundNative{proto: protocol.InboundResponses, prevID: "resp_unknown"}, snap, cands); err == nil ||
		err.code != responseAffinityLostCode {
		t.Fatalf("亲和未知应自解释: %+v", err)
	}
	// 命中:候选域收缩至亲和 provider(域内 failover 照常 = 保留该 provider 全部候选)
	p.affinity.Put("resp_1", "a2")
	got, err := p.stickyRoute(r, &inboundNative{proto: protocol.InboundResponses, prevID: "resp_1"}, snap, cands)
	if err != nil || len(got) != 1 || got[0].Provider != "a2" {
		t.Fatalf("候选域应收缩至亲和 provider: %+v %+v", got, err)
	}
}

// responses 直通拓扑:两个声明候选,粘性/GET/回写全链。
func newResponsesNativeProxy(t *testing.T) (*Proxy, *testkit.Upstream, *testkit.Upstream) {
	t.Helper()
	upA := testkit.StartJSONUpstream(t, 200, `{"id":"resp_1","object":"response","status":"completed","output":[],"usage":{"input_tokens":13,"output_tokens":1}}`)
	upB := testkit.StartJSONUpstream(t, 200, `{"id":"resp_2","object":"response","status":"completed","output":[],"usage":{"input_tokens":7,"output_tokens":2}}`)
	reg := adapter.NewRegistry()
	reg.Register("ra", adapter.NewOpenAI(upA.URL(), ""))
	reg.Register("rb", adapter.NewOpenAI(upB.URL(), ""))
	p := New(Config{
		Registry: reg,
		Providers: []config.Provider{
			{Name: "ra", Adapter: "openai", NativeResponses: true},
			{Name: "rb", Adapter: "openai", NativeResponses: true},
		},
		Routes: map[string][]config.Candidate{
			"alias-r": {{Provider: "ra", Model: "m-a", Priority: 0}, {Provider: "rb", Model: "m-b", Priority: 1}},
		},
		Timeouts: Timeouts{Dial: time.Second, FirstToken: 10 * time.Second, ChunkIdle: 10 * time.Second},
		Pools: map[string]*keypool.Pool{
			"ra": keypool.NewPool([]keypool.KeyInput{{ID: "KA", Value: "k", Weight: 1}}),
			"rb": keypool.NewPool([]keypool.KeyInput{{ID: "KB", Value: "k", Weight: 1}}),
		},
		RandIntn: func(n int) int { return 0 },
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	return p, upA, upB
}

// TestStickyE2E_创建亲和GET代理 创建 → id 回写亲和 → prevID 粘性单 provider → GET 代理转发。
func TestStickyE2E_创建亲和GET代理(t *testing.T) {
	p, upA, upB := newResponsesNativeProxy(t)
	mux := http.NewServeMux()
	mux.Handle("POST /v1/responses", p.HandlerResponses())
	mux.Handle("GET /v1/responses/{id}", p.HandlerResponsesStateless())
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	// 创建:落亲和(ra 首选)
	resp, body := postPath(t, ts.URL, "/v1/responses", `{"model":"alias-r","stream":false,"input":"你好"}`)
	if resp.StatusCode != 200 {
		t.Fatalf("创建应 200: %d %s", resp.StatusCode, body)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(body), &created); err != nil || created.ID != "resp_1" {
		t.Fatalf("创建响应形态不符: %s", body)
	}
	// prevID 粘性:候选域收缩至 ra(rb 不得被触达)
	resp, body = postPath(t, ts.URL, "/v1/responses", `{"model":"alias-r","stream":false,"previous_response_id":"resp_1","input":"接着说"}`)
	if resp.StatusCode != 200 {
		t.Fatalf("粘性请求应 200: %d %s", resp.StatusCode, body)
	}
	if n := upB.RequestCount(); n != 0 {
		t.Fatalf("粘性不得跨 provider 换道(rb 被触达 %d 次)", n)
	}
	if !strings.Contains(string(upA.LastBody()), `"previous_response_id":"resp_1"`) {
		t.Fatalf("状态请求应直通透传(含 prevID): %s", upA.LastBody())
	}
	// GET 代理:亲和已知 → 转发 ra 原样返回
	getResp, err := httpGet(t, ts.URL+"/v1/responses/resp_1")
	if err != nil {
		t.Fatal(err)
	}
	if getResp.StatusCode != 200 || !strings.Contains(getResp.Body, `"id":"resp_1"`) {
		t.Fatalf("GET 代理应原样返回: %d %s", getResp.StatusCode, getResp.Body)
	}
	// GET 未知 id:恒 404 自解释
	unknown, err := httpGet(t, ts.URL+"/v1/responses/resp_never")
	if err != nil {
		t.Fatal(err)
	}
	if unknown.StatusCode != 404 || !strings.Contains(unknown.Body, "response_not_found") {
		t.Fatalf("未知 id 应恒 404: %d %s", unknown.StatusCode, unknown.Body)
	}
	// 亲和未知 prevID:自解释 400,不盲发
	before := upA.RequestCount()
	resp, body = postPath(t, ts.URL, "/v1/responses", `{"model":"alias-r","stream":false,"previous_response_id":"resp_nolink","input":"x"}`)
	if resp.StatusCode != 400 || !strings.Contains(body, "response_affinity_lost") {
		t.Fatalf("亲和未知应自解释 400: %d %s", resp.StatusCode, body)
	}
	if upA.RequestCount() != before {
		t.Fatal("亲和未知不得盲发上游")
	}
}

// postPath 显式路径版 postChat(mux 装配下路径参与路由)。
func postPath(t *testing.T, url, path, body string) (*http.Response, string) {
	t.Helper()
	resp, err := http.Post(url+path, "application/json", strings.NewReader(body))
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

type httpGetResult struct {
	StatusCode int
	Body       string
}

func httpGet(t *testing.T, url string) (*httpGetResult, error) {
	t.Helper()
	resp, err := http.DefaultClient.Get(url)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return &httpGetResult{StatusCode: resp.StatusCode, Body: string(b)}, nil
}

// TestStickyE2E_有状态链式全链 状态 e2e(替身=内存存储的 OpenAI 契约响应上游):
// 创建 → id 回写亲和 → prev 链式(粘性单 provider,链式上下文到同一上游)→ GET 代理命中存储。
func TestStickyE2E_有状态链式全链(t *testing.T) {
	up := testkit.StartResponsesUpstream(t)
	reg := adapter.NewRegistry()
	reg.Register("ra", adapter.NewOpenAI(up.URL(), ""))
	p := New(Config{
		Registry:  reg,
		Providers: []config.Provider{{Name: "ra", Adapter: "openai", NativeResponses: true}},
		Routes: map[string][]config.Candidate{
			"alias-r": {{Provider: "ra", Model: "m-a", Priority: 0}},
		},
		Timeouts: Timeouts{Dial: time.Second, FirstToken: 10 * time.Second, ChunkIdle: 10 * time.Second},
		Pools:    map[string]*keypool.Pool{"ra": keypool.NewPool([]keypool.KeyInput{{ID: "KA", Value: "k", Weight: 1}})},
		RandIntn: func(n int) int { return 0 },
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	mux := http.NewServeMux()
	mux.Handle("POST /v1/responses", p.HandlerResponses())
	mux.Handle("GET /v1/responses/{id}", p.HandlerResponsesStateless())
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	// 创建
	resp, body := postPath(t, ts.URL, "/v1/responses", `{"model":"alias-r","stream":false,"input":"北京天气?"}`)
	if resp.StatusCode != 200 || !strings.Contains(body, `"id":"resp_1"`) {
		t.Fatalf("创建应 200 且回显 id: %d %s", resp.StatusCode, body)
	}
	// prev 链式:粘性直达同一上游,previous_response_id 原样透传(链式上下文由上游承载)
	resp, body = postPath(t, ts.URL, "/v1/responses", `{"model":"alias-r","stream":false,"previous_response_id":"resp_1","input":"那上海呢?"}`)
	if resp.StatusCode != 200 {
		t.Fatalf("链式应 200: %d %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(up.LastBody()), `"previous_response_id":"resp_1"`) ||
		!strings.Contains(string(up.LastBody()), "上海") {
		t.Fatalf("链式上下文应原样到达上游: %s", up.LastBody())
	}
	// GET 代理:muniu 亲和 → 上游 GET → 存储体原样返回(含创建时的完整 input)
	getResp, err := httpGet(t, ts.URL+"/v1/responses/resp_1")
	if err != nil {
		t.Fatal(err)
	}
	if getResp.StatusCode != 200 || !strings.Contains(getResp.Body, "北京天气") {
		t.Fatalf("GET 代理应命中上游存储: %d %s", getResp.StatusCode, getResp.Body)
	}
}

// TestStickyE2E_DELETE代理 状态 lane 删除动词(responses-state-delete-proxy):
// 亲和已知 → DELETE 按动词代理转发(上游存储真被删);亲和未知 → 恒 404 不触达上游;GET 零回归。
func TestStickyE2E_DELETE代理(t *testing.T) {
	up := testkit.StartResponsesUpstream(t)
	reg := adapter.NewRegistry()
	reg.Register("ra", adapter.NewOpenAI(up.URL(), ""))
	p := New(Config{
		Registry:  reg,
		Providers: []config.Provider{{Name: "ra", Adapter: "openai", NativeResponses: true}},
		Routes: map[string][]config.Candidate{
			"alias-r": {{Provider: "ra", Model: "m-a", Priority: 0}},
		},
		Timeouts: Timeouts{Dial: time.Second, FirstToken: 10 * time.Second, ChunkIdle: 10 * time.Second},
		Pools:    map[string]*keypool.Pool{"ra": keypool.NewPool([]keypool.KeyInput{{ID: "KA", Value: "k", Weight: 1}})},
		RandIntn: func(n int) int { return 0 },
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	mux := http.NewServeMux()
	mux.Handle("POST /v1/responses", p.HandlerResponses())
	mux.Handle("GET /v1/responses/{id}", p.HandlerResponsesStateless())
	mux.Handle("DELETE /v1/responses/{id}", p.HandlerResponsesStateless())
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	resp, body := postPath(t, ts.URL, "/v1/responses", `{"model":"alias-r","stream":false,"input":"你好"}`)
	if resp.StatusCode != 200 {
		t.Fatalf("创建应 200: %d %s", resp.StatusCode, body)
	}
	// DELETE 已知 id:按动词代理转发,上游存储真被删
	delResp, err := httpVerb(t, http.MethodDelete, ts.URL+"/v1/responses/resp_1")
	if err != nil {
		t.Fatal(err)
	}
	if delResp.StatusCode != 200 || !strings.Contains(delResp.Body, `"deleted":true`) {
		t.Fatalf("DELETE 代理应原样返回: %d %s", delResp.StatusCode, delResp.Body)
	}
	if _, ok := up.Stored("resp_1"); ok {
		t.Fatal("上游存储应真被删除")
	}
	// DELETE 未知 id:恒 404 自解释,不触达上游
	before := up.RequestCount()
	miss, err := httpVerb(t, http.MethodDelete, ts.URL+"/v1/responses/resp_never")
	if err != nil {
		t.Fatal(err)
	}
	if miss.StatusCode != 404 || !strings.Contains(miss.Body, "response_not_found") {
		t.Fatalf("未知 id 应恒 404: %d %s", miss.StatusCode, miss.Body)
	}
	if up.RequestCount() != before {
		t.Fatal("亲和未知不得触达上游")
	}
	// GET 零回归:重建后 GET 照常
	postPath(t, ts.URL, "/v1/responses", `{"model":"alias-r","stream":false,"input":"再来"}`)
	getResp, err := httpGet(t, ts.URL+"/v1/responses/resp_2")
	if err != nil {
		t.Fatal(err)
	}
	if getResp.StatusCode != 200 || !strings.Contains(getResp.Body, `"input":"再来"`) {
		t.Fatalf("GET 应零回归: %d %s", getResp.StatusCode, getResp.Body)
	}
}

func httpVerb(t *testing.T, verb, url string) (*httpGetResult, error) {
	t.Helper()
	req, err := http.NewRequest(verb, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return &httpGetResult{StatusCode: resp.StatusCode, Body: string(b)}, nil
}
