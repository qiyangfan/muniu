// 指标契约测试(gateway-parity「usage-metrics」):token 三向累加与落账同口径、
// 非流式不观测首字节、缓存判定三分支、禁用零开销等价、序列基数受配置界。
package proxy

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/solvela/muniu/internal/adapter"
	"github.com/solvela/muniu/internal/config"
	"github.com/solvela/muniu/internal/keypool"
	"github.com/solvela/muniu/internal/metering"
	"github.com/solvela/muniu/internal/metrics"
	"github.com/solvela/muniu/internal/protocol"
	"github.com/solvela/muniu/internal/semantic"
	"github.com/solvela/muniu/internal/store"
	"github.com/solvela/muniu/internal/testkit"
)

// renderMetrics 渲染当前指标全文(测试断言用)。
func renderMetrics() string {
	var b strings.Builder
	metrics.RenderTo(&b)
	return b.String()
}

// newMeteredProxy 带计量管道的单候选代理(usage 来自上游 JSON body)。
func newMeteredProxy(t *testing.T, jsonBody string) (*httptest.Server, *Proxy) {
	t.Helper()
	up := testkit.StartJSONUpstream(t, 200, jsonBody)
	reg := adapter.NewRegistry()
	reg.Register("prov-a", adapter.NewOpenAI(up.URL(), ""))
	db, err := store.Open(t.TempDir() + "/m.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	pipeline := metering.NewPipeline(db, metering.Options{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	p := New(Config{
		Registry: reg,
		Routes: map[string][]config.Candidate{
			"alias-a": {{Provider: "prov-a", Model: "up-a", Priority: 0, Weight: 1}},
		},
		Timeouts: Timeouts{Dial: time.Second, FirstToken: 10 * time.Second, ChunkIdle: 10 * time.Second},
		Pipeline: pipeline,
		Pools:    map[string]*keypool.Pool{"prov-a": keypool.NewPool([]keypool.KeyInput{{ID: "KA", Value: "k", Weight: 1}})},
		RandIntn: func(n int) int { return 0 },
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ts := httptest.NewServer(p.Handler())
	t.Cleanup(ts.Close)
	return ts, p
}

const usageBody = `{"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":10,"prompt_tokens_details":{"cached_tokens":20}}}`

// 禁用态零开销等价(渲染空)→ 启用后 token 三向累加且与落账行同口径;
// 非流式不观测首字节;序列恒 3 行(基数受配置界)。
func TestMetrics_Token三向累加_禁用等价_非流式无TTFB(t *testing.T) {
	metrics.SetEnabled(false)
	ts, _ := newMeteredProxy(t, usageBody)

	// 禁用态:请求行为正常,渲染输出空(禁用=无新序列且行为零变化)
	if resp, _ := postChat(t, ts.URL, chatBody); resp.StatusCode != 200 {
		t.Fatalf("禁用态请求应正常: %d", resp.StatusCode)
	}
	if got := renderMetrics(); got != "" {
		t.Fatalf("禁用态渲染应为空: %q", got)
	}

	// 启用:同口径三向累加(prompt=100/cached=20/completion=10)
	metrics.SetEnabled(true)
	t.Cleanup(func() { metrics.SetEnabled(false) })
	if resp, _ := postChat(t, ts.URL, chatBody); resp.StatusCode != 200 {
		t.Fatalf("请求应正常: %d", resp.StatusCode)
	}
	got := renderMetrics()
	assertMetricLine(t, got, `muniu_tokens_total{direction="prompt",provider="prov-a",model="alias-a"} 100`)
	assertMetricLine(t, got, `muniu_tokens_total{direction="cached_prompt",provider="prov-a",model="alias-a"} 20`)
	assertMetricLine(t, got, `muniu_tokens_total{direction="completion",provider="prov-a",model="alias-a"} 10`)
	if strings.Contains(got, "muniu_first_token_duration_ms_count 0") == false {
		t.Fatalf("非流式不得产生首字节观测(count 应为 0): %s", got)
	}
	if n := strings.Count(got, "muniu_tokens_total{"); n != 3 {
		t.Fatalf("token 序列应恒 3 行(基数界),实际 %d:\n%s", n, got)
	}
}

// 流式请求产生首字节观测。
func TestMetrics_流式首token观测(t *testing.T) {
	metrics.SetEnabled(true)
	t.Cleanup(func() { metrics.SetEnabled(false) })
	up := testkit.StartStreamUpstream(t, testkit.NormalEvents())
	reg := adapter.NewRegistry()
	reg.Register("prov-a", adapter.NewOpenAI(up.URL(), ""))
	mdb, err := store.Open(t.TempDir() + "/s.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mdb.Close() })
	p := New(Config{
		Registry: reg,
		Routes: map[string][]config.Candidate{
			"alias-a": {{Provider: "prov-a", Model: "up-a", Priority: 0}},
		},
		Timeouts: Timeouts{Dial: time.Second, FirstToken: 10 * time.Second, ChunkIdle: 10 * time.Second},
		Pipeline: metering.NewPipeline(mdb, metering.Options{}, slog.New(slog.NewTextHandler(io.Discard, nil))),
		Pools:    map[string]*keypool.Pool{"prov-a": keypool.NewPool([]keypool.KeyInput{{ID: "KA", Value: "k", Weight: 1}})},
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ts := httptest.NewServer(p.Handler())
	t.Cleanup(ts.Close)
	if resp, _ := postChat(t, ts.URL, streamChatBody); resp.StatusCode != 200 {
		t.Fatalf("流式应 200: %d", resp.StatusCode)
	}
	if got := renderMetrics(); !strings.Contains(got, "muniu_first_token_duration_ms_count 1") {
		t.Fatalf("流式应产生首字节观测(count=1): %s", got)
	}
}

// 缓存判定三分支:skip(租户开关关/tools 请求)、miss、hit(写入后同输入命中)。
func TestMetrics_缓存判定三分支(t *testing.T) {
	metrics.SetEnabled(true)
	t.Cleanup(func() { metrics.SetEnabled(false) })
	db, err := store.Open(t.TempDir() + "/c.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	c, err := semantic.NewCache(db, semantic.Options{
		Dim: 4, Threshold: 0.5, TTL: time.Hour, Capacity: 100, EmbeddingModel: "emb",
	}, constEmbedder{})
	if err != nil {
		t.Fatal(err)
	}
	p := New(Config{
		Routes: map[string][]config.Candidate{
			"alias-a": {{Provider: "prov-a", Model: "up-a", Priority: 0}},
		},
		Cache:        c,
		TenantSwitch: func(tenant string) bool { return tenant == "01900000-0000-7000-8000-000000000001" },
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)

	temp := 0.1
	plain := &protocol.ChatRequest{Model: "alias-a", Messages: msgOf(`[{"role":"user","content":"hi"}]`), Temperature: &temp}
	// skip:租户开关关
	if _, ph := p.lookupCacheFor("no-such-tenant", r, plain); ph != nil {
		t.Fatal("租户关应 skip(无 phase)")
	}
	// miss
	if _, ph := p.lookupCacheFor("01900000-0000-7000-8000-000000000001", r, plain); ph == nil {
		t.Fatal("首次检索应 miss(有 phase)")
	}
	// skip:tools 请求
	toolReq := &protocol.ChatRequest{Model: "alias-a", Messages: msgOf(`[{"role":"user","content":"hi"}]`), Temperature: &temp, Tools: json.RawMessage(`[]`)}
	if _, ph := p.lookupCacheFor("01900000-0000-7000-8000-000000000001", r, toolReq); ph != nil {
		t.Fatal("tools 请求应 skip")
	}
	// 写入后同输入 → hit
	_, ph := p.lookupCacheFor("01900000-0000-7000-8000-000000000001", r, plain)
	if err := p.cache.Store(r.Context(), "01900000-0000-7000-8000-000000000001", ph.fp, "alias-a", ph.vec,
		[]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)); err != nil {
		t.Fatal(err)
	}
	if res, _ := p.lookupCacheFor("01900000-0000-7000-8000-000000000001", r, plain); res == nil {
		t.Fatal("同输入应命中")
	}

	got := renderMetrics()
	assertMetricLine(t, got, `muniu_semantic_cache_queries_total{model="alias-a",result="skip"} 2`)
	assertMetricLine(t, got, `muniu_semantic_cache_queries_total{model="alias-a",result="miss"} 2`)
	assertMetricLine(t, got, `muniu_semantic_cache_queries_total{model="alias-a",result="hit"} 1`)
}

// ---- 夹具 ----

type constEmbedder struct{}

// Embed 恒返回同一归一化向量(同输入指纹精确命中;阈值 0.5 下语义路径同样收敛)。
func (constEmbedder) Embed(context.Context, string, string) ([]float32, error) {
	return []float32{1, 0, 0, 0}, nil
}

func msgOf(raw string) []protocol.Message {
	return []protocol.Message{{Role: "user", Content: json.RawMessage(raw)}}
}

// assertMetricLine 断言渲染文本含指定序列行(Prometheus 文本格式)。
func assertMetricLine(t *testing.T, rendered, line string) {
	t.Helper()
	for _, l := range strings.Split(rendered, "\n") {
		if strings.HasPrefix(l, line) {
			return
		}
	}
	t.Fatalf("渲染文本缺序列行 %q:\n%s", line, rendered)
}
