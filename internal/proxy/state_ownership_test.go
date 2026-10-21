// 状态 lane 归属隔离契约：亲和条目绑创建 key；跨 key GET/DELETE 恒 404 且上游零调用，
// DELETE 不可逆，绝不出站删他 key 状态；跨 key prevID 与亲和未知不可区分降级；
// nil KeyInfo 走 fail-closed；同 key 全链零变化。
// 测试键经 X-Test-Key 头注入，生产路径由 auth 中间件挂 ctx，语义一致。
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
	"github.com/solvela/muniu/internal/testkit"
)

// statefulUpstream 有状态上游测试接口，具体类型在 testkit 内未导出，方法集隐式满足。
type statefulUpstream interface {
	URL() string
	RequestCount() int
}

// newOwnershipProxy 双 key 注入网关：请求头 X-Test-Key 决定鉴权 key 身份，缺省头即无 KeyInfo。
func newOwnershipProxy(t *testing.T, up statefulUpstream) *httptest.Server {
	t.Helper()
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
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id := r.Header.Get("X-Test-Key"); id != "" {
			r = r.WithContext(auth.WithKeyInfo(r.Context(), &auth.KeyInfo{ID: id, TenantID: testTenant}))
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	return ts
}

func doWithKey(t *testing.T, ts *httptest.Server, method, path, keyID, body string) (*http.Response, string) {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, ts.URL+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if keyID != "" {
		req.Header.Set("X-Test-Key", keyID)
	}
	resp, err := http.DefaultClient.Do(req)
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

// 全链场景：同 key 创建、粘性链式、GET 全通；跨 key 三动词全部按未知拒绝且上游零调用。
func TestStateOwnership_跨key三拒绝与同key零变化(t *testing.T) {
	up := testkit.StartResponsesUpstream(t)
	ts := newOwnershipProxy(t, up)

	// keyA 创建，状态存其名下。
	resp, body := doWithKey(t, ts, http.MethodPost, "/v1/responses", "keyA", `{"model":"alias-r","stream":false,"input":"北京天气?"}`)
	if resp.StatusCode != 200 || !strings.Contains(body, `"id":"resp_1"`) {
		t.Fatalf("创建应 200: %d %s", resp.StatusCode, body)
	}
	baseline := up.RequestCount()

	// 跨 key GET：404 response_not_found，与未知不可区分，上游零调用。
	resp, body = doWithKey(t, ts, http.MethodGet, "/v1/responses/resp_1", "keyB", "")
	if resp.StatusCode != 404 || !strings.Contains(body, "response_not_found") {
		t.Fatalf("跨 key GET 应 404 response_not_found: %d %s", resp.StatusCode, body)
	}
	// 跨 key DELETE：404 且上游零调用，不可逆操作绝不出站删他 key 状态。
	resp, body = doWithKey(t, ts, http.MethodDelete, "/v1/responses/resp_1", "keyB", "")
	if resp.StatusCode != 404 || !strings.Contains(body, "response_not_found") {
		t.Fatalf("跨 key DELETE 应 404 response_not_found: %d %s", resp.StatusCode, body)
	}
	// 跨 key prevID：与亲和未知不可区分，按 response_affinity_lost 降级，上游零调用。
	resp, body = doWithKey(t, ts, http.MethodPost, "/v1/responses", "keyB",
		`{"model":"alias-r","stream":false,"previous_response_id":"resp_1","input":"接着说"}`)
	if resp.StatusCode != 400 || !strings.Contains(body, "response_affinity_lost") {
		t.Fatalf("跨 key prevID 应按未知降级: %d %s", resp.StatusCode, body)
	}
	if got := up.RequestCount(); got != baseline {
		t.Fatalf("跨 key 三动词均不得触达上游: 基线 %d,实际 %d", baseline, got)
	}

	// 同 key：GET/粘性/DELETE 全链零变化。
	resp, body = doWithKey(t, ts, http.MethodGet, "/v1/responses/resp_1", "keyA", "")
	if resp.StatusCode != 200 || !strings.Contains(body, "北京天气") {
		t.Fatalf("同 key GET 应代理上游存储: %d %s", resp.StatusCode, body)
	}
	resp, body = doWithKey(t, ts, http.MethodPost, "/v1/responses", "keyA",
		`{"model":"alias-r","stream":false,"previous_response_id":"resp_1","input":"那上海呢?"}`)
	if resp.StatusCode != 200 {
		t.Fatalf("同 key 粘性应 200: %d %s", resp.StatusCode, body)
	}
	resp, _ = doWithKey(t, ts, http.MethodDelete, "/v1/responses/resp_1", "keyA", "")
	if resp.StatusCode != 200 && resp.StatusCode != 404 {
		t.Fatalf("同 key DELETE 应代理上游(200/上游已删 404): %d", resp.StatusCode)
	}
}

// nil KeyInfo 生产不可达，auth 中间件保证；fail-closed 按未知处理，不留无主后门。
func TestStateOwnership_无鉴权信息failClosed(t *testing.T) {
	up := testkit.StartResponsesUpstream(t)
	ts := newOwnershipProxy(t, up)

	resp, body := doWithKey(t, ts, http.MethodPost, "/v1/responses", "keyA", `{"model":"alias-r","stream":false,"input":"hi"}`)
	if resp.StatusCode != 200 {
		t.Fatalf("创建应 200: %d %s", resp.StatusCode, body)
	}
	baseline := up.RequestCount()

	resp, body = doWithKey(t, ts, http.MethodGet, "/v1/responses/resp_1", "", "")
	if resp.StatusCode != 404 || !strings.Contains(body, "response_not_found") {
		t.Fatalf("无鉴权信息 GET 应 fail-closed 404: %d %s", resp.StatusCode, body)
	}
	resp, _ = doWithKey(t, ts, http.MethodDelete, "/v1/responses/resp_1", "", "")
	if resp.StatusCode != 404 {
		t.Fatalf("无鉴权信息 DELETE 应 fail-closed 404: %d", resp.StatusCode)
	}
	if got := up.RequestCount(); got != baseline {
		t.Fatalf("fail-closed 路径不得触达上游: %d", got)
	}
}
