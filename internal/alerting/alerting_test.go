// 告警通知器契约测试(webhook-alerting;测试先行域):
// 去重窗口内静默/窗口过期重新通知/退避重试后放弃/失败不波及调用方/payload 形态。
package alerting

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

type clock struct{ t time.Time }

func (c *clock) Now() time.Time { return c.t }

func TestAlerting_去重窗口(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
	}))
	t.Cleanup(srv.Close)
	clk := &clock{t: time.Now()}
	n := New(Options{WebhookURL: srv.URL, DedupWindow: time.Minute, Backoff: []time.Duration{time.Millisecond}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: clk.Now})

	n.Emit("keypool_exhausted:p1", "warning", "x")
	n.Emit("keypool_exhausted:p1", "warning", "x") // 窗口内:静默
	n.Emit("keypool_exhausted:p2", "warning", "y") // 不同键:通知
	waitFor(t, func() bool { return hits.Load() == 2 })

	clk.t = clk.t.Add(2 * time.Minute) // 窗口过期
	n.Emit("keypool_exhausted:p1", "warning", "x")
	waitFor(t, func() bool { return hits.Load() == 3 })
}

func TestAlerting_退避重试后放弃(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	n := New(Options{WebhookURL: srv.URL, Backoff: []time.Duration{time.Millisecond, time.Millisecond}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})

	n.Emit("metering_sink_down", "critical", "x")
	waitFor(t, func() bool { return hits.Load() == 3 }) // 初次 + 2 次退避
	time.Sleep(20 * time.Millisecond)
	if got := hits.Load(); got != 3 {
		t.Fatalf("放弃后不得再试: %d", got)
	}
}

func TestAlerting_payload形态(t *testing.T) {
	type payload struct {
		Event      string `json:"event"`
		Severity   string `json:"severity"`
		Summary    string `json:"summary"`
		OccurredAt string `json:"occurred_at"`
	}
	ch := make(chan payload, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p payload
		_ = json.NewDecoder(r.Body).Decode(&p)
		ch <- p
	}))
	t.Cleanup(srv.Close)
	n := New(Options{WebhookURL: srv.URL, Backoff: []time.Duration{time.Millisecond}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	n.Emit("all_candidates_failed", "critical", "别名 alias-a 全部候选失败")
	p := <-ch
	if p.Event != "all_candidates_failed" || p.Severity != "critical" || p.Summary == "" || p.OccurredAt == "" {
		t.Fatalf("payload 形态不符: %+v", p)
	}
}

func TestAlerting_nil零开销(t *testing.T) {
	var n *Notifier
	n.Emit("anything", "warning", "x") // nil 接收者:不得 panic
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("条件未在时限内达成")
}
