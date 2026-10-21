// failover 循环集成测试，判定表驱动：每个剧本三项断言——客户端字节序列、usage 行、key 池状态。
// 覆盖 429 换道、5xx 换道、断流后池只记 EMA 不冷却、全池冷却耗尽、断连计量。
package proxy

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/solvela/muniu/internal/adapter"
	"github.com/solvela/muniu/internal/adapter/bridge"
	"github.com/solvela/muniu/internal/config"
	"github.com/solvela/muniu/internal/keypool"
	"github.com/solvela/muniu/internal/metering"
	"github.com/solvela/muniu/internal/store"
	"github.com/solvela/muniu/internal/testkit"
)

// foHarness 双上游 failover 测试台：返回 server、两个 key 池与计量断言库。
type foHarness struct {
	ts       *httptest.Server
	poolA    *keypool.Pool
	poolB    *keypool.Pool
	db       *sql.DB
	pipeline *metering.Pipeline
}

func newFoHarness(t *testing.T, upA *testkit.Upstream, statusB int, bodyB string, streamB []testkit.Event) *foHarness {
	t.Helper()
	reg := adapter.NewRegistry()
	reg.Register("prov-a", adapter.NewOpenAI(upA.URL(), ""))
	var poolB *keypool.Pool
	var bURL string
	if streamB != nil {
		upB := testkit.StartStreamUpstream(t, streamB)
		bURL = upB.URL()
	} else {
		upB := testkit.StartJSONUpstream(t, statusB, bodyB)
		bURL = upB.URL()
	}
	reg.Register("prov-b", adapter.NewOpenAI(bURL, ""))

	poolA := keypool.NewPool([]keypool.KeyInput{{ID: "KA", Value: "sk-a", Weight: 1}})
	poolB = keypool.NewPool([]keypool.KeyInput{{ID: "KB", Value: "sk-b", Weight: 1}})

	db, err := store.Open(filepath.Join(t.TempDir(), "fo.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	pipeline := metering.NewPipeline(db, metering.Options{Interval: time.Hour, Batch: 1 << 20, Capacity: 64},
		slog.New(slog.NewTextHandler(io.Discard, nil)))

	p := New(Config{
		Registry: reg,
		Routes: map[string][]config.Candidate{
			"alias-a": {
				{Provider: "prov-a", Model: "up-a", Priority: 0, Weight: 1},
				{Provider: "prov-b", Model: "up-b", Priority: 1, Weight: 1},
			},
		},
		Pricing: mustDir(t, map[string]metering.Pricing{
			"alias-a": {Currency: "CNY", InputCreditsPerM: 2_000_000, OutputCreditsPerM: 8_000_000},
		}),
		Timeouts: Timeouts{Dial: time.Second, FirstToken: 10 * time.Second, ChunkIdle: 10 * time.Second},
		Pipeline: pipeline,
		Pools:    map[string]*keypool.Pool{"prov-a": poolA, "prov-b": poolB},
		RandIntn: func(n int) int { return 0 }, // 起点确定为首候选
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ts := httptest.NewServer(p.Handler())
	t.Cleanup(ts.Close)
	return &foHarness{ts: ts, poolA: poolA, poolB: poolB, db: db, pipeline: pipeline}
}

// flush 排干计量管道，断言落库前必须调用；管道批量窗口为小时级以防测试抖动。
func (h *foHarness) flush() {
	_ = h.pipeline.Close()
}

// mustDir 构造静态价目目录，无分时窗口恒用缺省价。
func mustDir(t *testing.T, defs map[string]metering.Pricing) *metering.PricingDirectory {
	t.Helper()
	tables := make(map[string]*metering.PricingTable, len(defs))
	for alias, def := range defs {
		tbl, err := metering.NewPricingTable(def, time.UTC, nil)
		if err != nil {
			t.Fatal(err)
		}
		tables[alias] = tbl
	}
	return metering.NewPricingDirectory(tables)
}

func foPost(t *testing.T, ts *httptest.Server, body string) (int, string) {
	t.Helper()
	resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode, string(b)
}

// waitRows 轮询等待计量异步批量落库。
func waitRows(t *testing.T, db *sql.DB, want int) []store.UsageRow {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		var rows []store.UsageRow
		q := `SELECT request_id, provider, model_alias, upstream_model, stream, status,
			IFNULL(error_class,''), prompt_tokens, completion_tokens, estimated, cost_credits, currency
			FROM usage_logs`
		rs, err := db.Query(q)
		if err != nil {
			t.Fatal(err)
		}
		for rs.Next() {
			var r store.UsageRow
			var stream int64
			if err := rs.Scan(&r.RequestID, &r.Provider, &r.ModelAlias, &r.UpstreamModel, &stream,
				&r.Status, &r.ErrorClass, &r.PromptTokens, &r.CompletionTokens,
				&r.Estimated, &r.CostCredits, &r.Currency); err != nil {
				t.Fatal(err)
			}
			r.Stream = stream == 1
			rows = append(rows, r)
		}
		_ = rs.Close()
		if len(rows) >= want {
			return rows
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待 %d 行计量超时,实际 %d", want, len(rows))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestFailover_429换道_三断言(t *testing.T) {
	// A 上游 429 后冷却，降级 priority 1 的 B 成功。
	h := newFoHarness(t,
		testkit.StartJSONUpstream(t, 429, `{"error":{"message":"rate limited"}}`),
		200, testkit.JSONOKBody, nil)

	code, body := foPost(t, h.ts, `{"model":"alias-a","messages":[{"role":"user","content":"hi"}]}`)
	now := time.Now()
	h.flush()
	// 断言 1：客户端字节序列为 B 的成功响应。
	if code != 200 || body != testkit.JSONOKBody {
		t.Fatalf("应换道到 B 成功: %d %s", code, body)
	}
	// 断言 2：key 池状态为 A 因 429 冷却、B 健康。
	if st := h.poolA.StateOf("KA", now); st != "cooldown" {
		t.Fatalf("A key 应冷却,实际 %s", st)
	}
	if st := h.poolB.StateOf("KB", now); st != "healthy" {
		t.Fatalf("B key 应健康,实际 %s", st)
	}
	// 断言 3：usage 行为 A 的 429 错误行与 B 的成功行各一。
	rows := waitRows(t, h.db, 2)
	byProv := map[string]store.UsageRow{}
	for _, r := range rows {
		byProv[r.Provider] = r
	}
	ra, okA := byProv["prov-a"]
	rb, okB := byProv["prov-b"]
	if !okA || !okB {
		t.Fatalf("应各有一行计量: %+v", rows)
	}
	if ra.Status != "error" || ra.ErrorClass != "rate_limited_429" {
		t.Fatalf("A 行应为 429 错误: %+v", ra)
	}
	if rb.Status != "ok" || rb.Provider != "prov-b" || rb.Estimated {
		t.Fatalf("B 行应为成功: %+v", rb)
	}
}

func TestFailover_5xx换道_三断言(t *testing.T) {
	h := newFoHarness(t,
		testkit.StartJSONUpstream(t, 500, testkit.JSON500Body),
		200, testkit.JSONOKBody, nil)

	code, body := foPost(t, h.ts, `{"model":"alias-a","messages":[{"role":"user","content":"hi"}]}`)
	now := time.Now()
	h.flush()
	if code != 200 || body != testkit.JSONOKBody {
		t.Fatalf("应换道到 B 成功: %d %s", code, body)
	}
	if st := h.poolA.StateOf("KA", now); st != "healthy" {
		t.Fatalf("单次 5xx 不应冷却,实际 %s", st)
	}
	rows := waitRows(t, h.db, 2)
	if rows[0].Provider == "prov-a" && rows[0].Status != "error" {
		t.Fatalf("A 行应错误: %+v", rows[0])
	}
}

func TestFailover_断流断言_池只记EMA不冷却(t *testing.T) {
	events := testkit.MidStreamCutEvents()
	up := testkit.StartStreamUpstream(t, events)
	reg := adapter.NewRegistry()
	reg.Register("prov-a", adapter.NewOpenAI(up.URL(), ""))
	poolA := keypool.NewPool([]keypool.KeyInput{{ID: "KA", Value: "sk-a", Weight: 1}})
	db, err := store.Open(filepath.Join(t.TempDir(), "cut.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	pipeline := metering.NewPipeline(db, metering.Options{Interval: time.Hour, Batch: 1 << 20, Capacity: 64},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	p := New(Config{
		Registry: reg,
		Routes: map[string][]config.Candidate{
			"alias-a": {{Provider: "prov-a", Model: "up-a", Priority: 0, Weight: 1}},
		},
		Timeouts: Timeouts{Dial: time.Second, FirstToken: 10 * time.Second, ChunkIdle: 10 * time.Second},
		Pipeline: pipeline,
		Pools:    map[string]*keypool.Pool{"prov-a": poolA},
		RandIntn: func(n int) int { return 0 },
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ts := httptest.NewServer(p.Handler())
	t.Cleanup(ts.Close)

	// 断言 1：客户端字节为已转发部分加断言出口，即错误帧与 [DONE]。
	_, body := foPost(t, ts, `{"model":"alias-a","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if !strings.HasSuffix(body, "data: [DONE]\n\n") || !strings.Contains(body, `"code":"mid_stream_fail"`) {
		t.Fatalf("断言出口形态不符: %q", body)
	}
	now := time.Now()
	_ = pipeline.Close()
	// 断言 2：池不冷却，mid_stream 只记 EMA 失败。
	if st := poolA.StateOf("KA", now); st != "healthy" {
		t.Fatalf("断流不得冷却,实际 %s", st)
	}
	if r := poolA.EMARate("KA"); r > 0.99 {
		t.Fatalf("EMA 应记录失败,实际 %v", r)
	}
	// 断言 3：usage 行为 error、mid_stream_fail、estimated，已转发部分按估算落账。
	rows := waitRows(t, db, 1)
	if rows[0].Status != "error" || rows[0].ErrorClass != "mid_stream_fail" || !rows[0].Estimated {
		t.Fatalf("断流计量行不符: %+v", rows[0])
	}
}

func TestFailover_全候选key池冷却_耗尽信封(t *testing.T) {
	h := newFoHarness(t,
		testkit.StartJSONUpstream(t, 429, `{"error":{"message":"rate limited"}}`),
		429, `{"error":{"message":"rate limited"}}`, nil)
	// 预先把两池 key 全部打入长冷却
	now := time.Now()
	h.poolA.Report(&keypool.Key{ID: "KA"}, keypool.OutcomeRateLimited, now, time.Hour)
	h.poolB.Report(&keypool.Key{ID: "KB"}, keypool.OutcomeRateLimited, now, time.Hour)

	code, body := foPost(t, h.ts, `{"model":"alias-a","messages":[{"role":"user","content":"hi"}]}`)
	if code != 502 || !strings.Contains(body, "all_candidates_failed") {
		t.Fatalf("全池冷却应 502 all_candidates_failed: %d %s", code, body)
	}
}

func TestFailover_断连计量client_cancel(t *testing.T) {
	up := testkit.StartStreamUpstream(t, testkit.SlowChunkEvents(500*time.Millisecond))
	reg := adapter.NewRegistry()
	reg.Register("prov-a", adapter.NewOpenAI(up.URL(), ""))
	poolA := keypool.NewPool([]keypool.KeyInput{{ID: "KA", Value: "sk-a", Weight: 1}})
	db, err := store.Open(filepath.Join(t.TempDir(), "dc.db"))
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
		Pools:    map[string]*keypool.Pool{"prov-a": poolA},
		RandIntn: func(n int) int { return 0 },
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ts := httptest.NewServer(p.Handler())
	t.Cleanup(ts.Close)

	// 客户端读完响应头即断连
	ctx, cancel := contextWithCancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", ts.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"alias-a","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	_, _ = resp.Body.Read(buf)
	cancel()
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	// 不主动 Close：断连时 handler 可能仍在卸载，Close 会丢行；依赖 500ms 批量窗口自然落库。
	rows := waitRows(t, db, 1)
	if rows[0].Status != "client_cancel" {
		t.Fatalf("断连应记 client_cancel: %+v", rows[0])
	}
}

func contextWithCancel() (context.Context, context.CancelFunc) {
	return context.WithCancel(context.Background())
}

func TestEmbeddings_透传与计量(t *testing.T) {
	const embBody = `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2]}],"model":"up-emb","usage":{"prompt_tokens":9,"total_tokens":9}}`
	up := testkit.StartJSONUpstream(t, 200, embBody)
	reg := adapter.NewRegistry()
	reg.Register("prov-a", adapter.NewOpenAI(up.URL(), ""))
	poolA := keypool.NewPool([]keypool.KeyInput{{ID: "KA", Value: "sk-a", Weight: 1}})
	db, err := store.Open(filepath.Join(t.TempDir(), "emb.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	pipeline := metering.NewPipeline(db, metering.Options{Interval: time.Hour, Batch: 1 << 20, Capacity: 64},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	p := New(Config{
		Registry: reg,
		Routes: map[string][]config.Candidate{
			"emb-alias": {{Provider: "prov-a", Model: "up-emb", Priority: 0, Weight: 1}},
		},
		// 仅 input 价，output=0，成本只算输入段。
		Pricing: mustDir(t, map[string]metering.Pricing{
			"emb-alias": {Currency: "CNY", InputCreditsPerM: 2_000_000},
		}),
		Timeouts: Timeouts{Dial: time.Second, FirstToken: 5 * time.Second, ChunkIdle: 5 * time.Second},
		Pipeline: pipeline,
		Pools:    map[string]*keypool.Pool{"prov-a": poolA},
		RandIntn: func(n int) int { return 0 },
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ts := httptest.NewServer(p.HandlerEmbeddings())
	t.Cleanup(ts.Close)

	code, body := foPost(t, ts, `{"model":"emb-alias","input":"你好","vendor_ext":{"k":1}}`)
	if code != 200 || body != embBody {
		t.Fatalf("embeddings 透传不符: %d %s", code, body)
	}
	_ = pipeline.Close()
	rows := waitRows(t, db, 1)
	if rows[0].PromptTokens != 9 || rows[0].CompletionTokens != 0 {
		t.Fatalf("embedding 计量不符: %+v", rows[0])
	}
	// 仅输入段计价：ceil(9×2)=18μ。
	if rows[0].CostCredits != 18 {
		t.Fatalf("embedding 成本不符: %d", rows[0].CostCredits)
	}
}

func TestEmbeddings_不支持与未知别名(t *testing.T) {
	const embBody = `{"object":"list","data":[],"model":"up-emb","usage":{"prompt_tokens":0,"total_tokens":0}}`
	// 桥接型无 embeddings 能力
	up := testkit.StartJSONUpstream(t, 200, embBody)
	reg := adapter.NewRegistry()
	reg.Register("prov-a", adapter.NewOpenAI(up.URL(), ""))
	reg.Register("anthropic", bridge.NewAnthropic("https://api.anthropic.com", adapter.Options{}))
	poolA := keypool.NewPool([]keypool.KeyInput{{ID: "KA", Value: "v", Weight: 1}})
	p := New(Config{
		Registry: reg,
		Routes: map[string][]config.Candidate{
			"emb-alias": {{Provider: "prov-a", Model: "up-emb", Priority: 0, Weight: 1}},
			"claude":    {{Provider: "anthropic", Model: "claude-x", Priority: 0, Weight: 1}},
		},
		Pools:  map[string]*keypool.Pool{"prov-a": poolA, "anthropic": poolA},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ts := httptest.NewServer(p.HandlerEmbeddings())
	t.Cleanup(ts.Close)

	code, body := foPost(t, ts, `{"model":"claude","input":"x"}`)
	if code != 400 || !strings.Contains(body, "embeddings_not_supported") {
		t.Fatalf("桥接应报不支持: %d %s", code, body)
	}
	code, body = foPost(t, ts, `{"model":"no-such","input":"x"}`)
	if code != 400 {
		t.Fatalf("未知别名应 400: %d %s", code, body)
	}
}
