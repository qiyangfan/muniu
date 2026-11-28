package proxy

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/solvela/muniu/internal/adapter"
	"github.com/solvela/muniu/internal/config"
	"github.com/solvela/muniu/internal/keypool"
	"github.com/solvela/muniu/internal/metering"
	"github.com/solvela/muniu/internal/store"
)

// TestFailover_首token前总预算 两个候选都挂起时,首 token 前 deadline 命中 504,
// 且不再发起第二个候选。
func TestFailover_首token前总预算(t *testing.T) {
	slow := func() *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(300 * time.Millisecond)
			w.WriteHeader(200)
		}))
	}
	upA, upB := slow(), slow()
	defer upA.Close()
	defer upB.Close()
	reg := adapter.NewRegistry()
	reg.Register("prov-a", adapter.NewOpenAI(upA.URL, ""))
	reg.Register("prov-b", adapter.NewOpenAI(upB.URL, ""))
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "dl.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	pipeline := metering.NewPipeline(context.Background(), db, metering.Options{Interval: time.Hour, Batch: 1 << 20, Capacity: 64},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	p := New(Config{
		Registry: reg,
		Routes: map[string][]config.Candidate{
			"alias-a": {
				{Provider: "prov-a", Model: "up-a", Priority: 0, Weight: 1},
				{Provider: "prov-b", Model: "up-b", Priority: 1, Weight: 1},
			},
		},
		Pricing: mustDir(t, map[string]metering.Pricing{"alias-a": {Currency: "CNY", InputCreditsPerM: 1, OutputCreditsPerM: 1}}),
		Timeouts: Timeouts{Dial: time.Second, FirstToken: 10 * time.Second, ChunkIdle: 10 * time.Second,
			FailoverDeadline: 50 * time.Millisecond},
		Pipeline: pipeline,
		Pools: map[string]*keypool.Pool{
			"prov-a": keypool.NewPool([]keypool.KeyInput{{ID: "a", Value: "sk-a"}}),
			"prov-b": keypool.NewPool([]keypool.KeyInput{{ID: "b", Value: "sk-b"}}),
		},
		RandIntn: func(int) int { return 0 },
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ts := httptest.NewServer(p.Handler())
	defer ts.Close()
	code, body := foPost(t, ts, `{"model":"alias-a","messages":[{"role":"user","content":"hi"}]}`)
	if code != 504 || !strings.Contains(body, "failover_budget_exhausted") {
		t.Fatalf("预算命中应 504: %d %s", code, body)
	}
}
