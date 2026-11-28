package proxy

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/solvela/muniu/internal/adapter"
	"github.com/solvela/muniu/internal/config"
	"github.com/solvela/muniu/internal/failover"
	"github.com/solvela/muniu/internal/keypool"
	"github.com/solvela/muniu/internal/metering"
	"github.com/solvela/muniu/internal/store"
	"github.com/solvela/muniu/internal/testkit"
)

// TestFailover_同provider换key key 级 429 必须换同 provider 的另一把 key,
// 而不是直接换下一个 provider/失败。
func TestFailover_同provider换key(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer sk-1" {
			w.Header().Set("Retry-After", "60")
			w.WriteHeader(429)
			_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(testkit.JSONOKBody))
	}))
	defer up.Close()
	reg := adapter.NewRegistry()
	reg.Register("prov-a", adapter.NewOpenAI(up.URL, ""))
	pool := keypool.NewPool([]keypool.KeyInput{{ID: "k1", Value: "sk-1"}, {ID: "k2", Value: "sk-2"}})
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "ev.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	pipeline := metering.NewPipeline(context.Background(), db, metering.Options{Interval: time.Hour, Batch: 1 << 20, Capacity: 64},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	p := New(Config{
		Registry: reg,
		Routes: map[string][]config.Candidate{
			"alias-a": {{Provider: "prov-a", Model: "up-a", Priority: 0, Weight: 1}},
		},
		Pricing:  mustDir(t, map[string]metering.Pricing{"alias-a": {Currency: "CNY", InputCreditsPerM: 1, OutputCreditsPerM: 1}}),
		Timeouts: Timeouts{Dial: time.Second, FirstToken: 10 * time.Second, ChunkIdle: 10 * time.Second, MaxAttempts: 4},
		Pipeline: pipeline,
		Pools:    map[string]*keypool.Pool{"prov-a": pool},
		RandIntn: func(n int) int { return 0 },
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ts := httptest.NewServer(p.Handler())
	defer ts.Close()
	code, body := foPost(t, ts, `{"model":"alias-a","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 {
		t.Fatalf("应换 key 成功: %d %s", code, body)
	}
	if pool.StateOf("k1", time.Now()) != "cooldown" {
		t.Fatal("429 key 应冷却")
	}
	if pool.StateOf("k2", time.Now()) != "healthy" {
		t.Fatal("第二把 key 应健康")
	}
}

// TestFailover_400不降key 请求级 400 不得写 key EMA(ADR-0027)。
func TestFailover_400不降key(t *testing.T) {
	h := newFoHarness(t, testkit.StartJSONUpstream(t, 400, `{"error":{"message":"bad"}}`), 200, testkit.JSONOKBody, nil)
	before := h.poolA.EMARate("KA")
	code, _ := foPost(t, h.ts, `{"model":"alias-a","messages":[{"role":"user","content":"hi"}]}`)
	if code != 400 {
		t.Fatalf("400 应直接返回: %d", code)
	}
	if got := h.poolA.EMARate("KA"); got != before {
		t.Fatalf("400 不得改 key EMA: before=%v after=%v", before, got)
	}
}

func TestKeyOutcomeOf_作用域表(t *testing.T) {
	cases := []struct {
		class  failover.ErrorClass
		out    keypool.Outcome
		report bool
	}{
		{failover.ClassRateLimited, keypool.OutcomeRateLimited, true},
		{failover.ClassAuthFailed, keypool.OutcomeAuthFailed, true},
		{failover.ClassUpstream5xx, keypool.OutcomeServerError, true},
		{failover.ClassTimeoutFirst, keypool.OutcomeFailure, true},
		{failover.ClassBadRequest, 0, false},
		{failover.ClassUnknown, 0, false},
	}
	for _, c := range cases {
		got, report := keyOutcomeOf(c.class)
		if report != c.report || (report && got != c.out) {
			t.Fatalf("%s 归因失真: got=%v/%v want=%v/%v", c.class, got, report, c.out, c.report)
		}
	}
}
