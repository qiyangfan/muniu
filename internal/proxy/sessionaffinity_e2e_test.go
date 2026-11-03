// 会话缓存亲和端到端(等权双候选+注入确定性随机源,评审 C4 敏感性重构):
// 无亲和时起点恒 pa(注入 r=0,五拍剧本基线锚),flaky 上游驱动「失败即清」哨兵
// (移除 drop 必红,评审 C1);起点语义突变敏感性由 failover iterator 单测钉死(C4 分层)。
// 让位轮不改写条目、零证据不建粘两哨兵保留。
package proxy

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/solvela/muniu/internal/adapter"
	"github.com/solvela/muniu/internal/config"
	"github.com/solvela/muniu/internal/keypool"
	"github.com/solvela/muniu/internal/testkit"
)

// flakyStreamUpstream 可控失败流上游:fault=true 返回 500(首 token 前失败,可安全换道),
// 否则回放带 cached_tokens 的最小事件流(worth 证据成立)。
type flakyStreamUpstream struct {
	*httptest.Server
	fault atomic.Bool
	hits  atomic.Int64
}

func newFlakyStreamUpstream(t *testing.T) *flakyStreamUpstream {
	t.Helper()
	f := &flakyStreamUpstream{}
	frame := func(payload string) string { return "data: " + payload + "\n\n" }
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		if f.fault.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"message":"boom"}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			frame(`{"id":"c","choices":[{"index":0,"delta":{"role":"assistant"}}]}`) +
				frame(`{"id":"c","choices":[],"usage":{"prompt_tokens":9,"cached_tokens":8,"completion_tokens":2}}`) +
				"data: [DONE]\n\n"))
	}))
	t.Cleanup(f.Close)
	return f
}

func newAffinityE2EProxy(t *testing.T, upA, upB string) (*Proxy, *httptest.Server) {
	t.Helper()
	reg := adapter.NewRegistry()
	reg.Register("pa", adapter.NewOpenAI(upA, "sk-a"))
	reg.Register("pb", adapter.NewOpenAI(upB, "sk-b"))
	p := New(Config{
		Registry: reg,
		Providers: []config.Provider{
			{Name: "pa", Adapter: "openai", BaseURL: upA},
			{Name: "pb", Adapter: "openai", BaseURL: upB},
		},
		Routes: map[string][]config.Candidate{
			"alias-a": {
				// 等权同组:注入 r=0 时无亲和起点恒 pa(拍1/拍5 基线锚)
				{Provider: "pa", Model: "m", Priority: 0, Weight: 1},
				{Provider: "pb", Model: "m", Priority: 0, Weight: 1},
			},
		},
		RandIntn: func(int) int { return 0 }, // 确定性:无亲和起点恒 pa(五拍剧本的基线锚)
		Timeouts: defTimeouts(),
		Pools: map[string]*keypool.Pool{
			"pa": keypool.NewPool([]keypool.KeyInput{{ID: "KA", Value: "sk-a", Weight: 1}}),
			"pb": keypool.NewPool([]keypool.KeyInput{{ID: "KB", Value: "sk-b", Weight: 1}}),
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ts := httptest.NewServer(p.Handler())
	t.Cleanup(ts.Close)
	return p, ts
}

func postSticky(t *testing.T, url, session string) int {
	t.Helper()
	req, err := http.NewRequest("POST", url+"/v1/chat/completions", strings.NewReader(streamChatBody))
	if err != nil {
		t.Fatalf("构造请求: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Session-Id", session)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

func TestSessionAffinity_端到端粘住让位失败即清(t *testing.T) {
	upA := newFlakyStreamUpstream(t)
	upB := newFlakyStreamUpstream(t) // 带缓存证据,换道轮可建/续粘
	p, ts := newAffinityE2EProxy(t, upA.URL, upB.URL)
	sid := "h::s1"

	// 拍1:无亲和起点恒 pa(r=0),worth 证据成立 → 首建粘 pa
	if code := postSticky(t, ts.URL, "s1"); code != 200 {
		t.Fatalf("拍1 状态码 %d", code)
	}
	if prov, _, st := p.cacheAffinity.lookup(sid, "", time.Now()); st != "hit" || prov != "pa" {
		t.Fatalf("拍1 应粘 pa: %s %s", prov, st)
	}

	// 拍2:pa 池键冷却让位 → 跳过 pa 转 pb 服务;条目必须保留不改写(恢复即复粘)
	paPool := p.topo.Load().pools["pa"]
	paPool.Report(&keypool.Key{ID: "KA"}, keypool.OutcomeRateLimited, time.Now(), 400*time.Millisecond)
	if code := postSticky(t, ts.URL, "s1"); code != 200 {
		t.Fatalf("拍2 状态码 %d", code)
	}
	if prov, _, st := p.cacheAffinity.lookup(sid, "", time.Now()); st != "hit" || prov != "pa" {
		t.Fatalf("冷却让位不得清条/改写(保留语义): %s %s", prov, st)
	}

	// 拍3:冷却解除 → 复粘 pa
	time.Sleep(500 * time.Millisecond)
	beforeA := upA.hits.Load()
	if code := postSticky(t, ts.URL, "s1"); code != 200 {
		t.Fatalf("拍3 状态码 %d", code)
	}
	if upA.hits.Load() <= beforeA {
		t.Fatal("冷却解除后应复粘 pa")
	}

	// 拍4:pa 翻脸(500)→ stick 尝试失败 → 换道 pb;条目必须被清(失败即清,C1 哨兵:
	// 移除循环内 drop 接线,本拍断言必红)
	upA.fault.Store(true)
	if code := postSticky(t, ts.URL, "s1"); code != 200 {
		t.Fatalf("拍4 状态码 %d", code)
	}
	if _, _, st := p.cacheAffinity.lookup(sid, "", time.Now()); st != "miss" {
		t.Fatalf("stick 失败必须清条: %s", st)
	}

	// 拍5:pa 恢复 → 无条目起点恒 pa → pa 服务并按「无条目首建粘当次服务者」重建
	upA.fault.Store(false)
	if code := postSticky(t, ts.URL, "s1"); code != 200 {
		t.Fatalf("拍5 状态码 %d", code)
	}
	if prov, _, _ := p.cacheAffinity.lookup(sid, "", time.Now()); prov != "pa" {
		t.Fatalf("拍5 应无条目首建粘 pa: %s", prov)
	}
}

func TestSessionAffinity_零证据不建粘(t *testing.T) {
	frame := func(payload string) string { return "data: " + payload + "\n\n" }
	events := []testkit.Event{
		{Data: frame(`{"id":"z","choices":[{"index":0,"delta":{"role":"assistant"}}]}`)},
		{Data: frame(`{"id":"z","choices":[],"usage":{"prompt_tokens":9,"cached_tokens":0,"completion_tokens":2}}`)},
		{Data: "data: [DONE]\n\n"},
	}
	upA := testkit.StartStreamUpstream(t, events)
	upB := testkit.StartJSONUpstream(t, 200, "{}")
	p, ts := newAffinityE2EProxy(t, upA.URL(), upB.URL())
	if code := postSticky(t, ts.URL, "z1"); code != 200 {
		t.Fatalf("状态码 %d", code)
	}
	if _, _, st := p.cacheAffinity.lookup("h::z1", "", time.Now()); st != "miss" {
		t.Fatalf("零缓存证据不得建粘: %s", st)
	}
}
