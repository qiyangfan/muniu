// 契约覆盖：去重窗口内静默、窗口过期重新通知、退避重试后放弃、失败不波及调用方、payload 形态。
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
	n.Emit("keypool_exhausted:p1", "warning", "x") // 窗口内：静默
	n.Emit("keypool_exhausted:p2", "warning", "y") // 不同键：通知
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
	n.Emit("anything", "warning", "x") // nil 接收者不得 panic
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

func TestAlerting_终局回调失败与成功(t *testing.T) {
	var fails atomic.Int32
	failCh := make(chan DeliveryFailure, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	n := New(Options{WebhookURL: srv.URL, Backoff: []time.Duration{time.Millisecond},
		Logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		OnDeliveryFailure: func(f DeliveryFailure) { fails.Add(1); failCh <- f }})

	n.Emit("e", "warning", "s")
	waitFor(t, func() bool { return fails.Load() == 1 })
	got := <-failCh // 通道传递规避跨 goroutine 数据竞态
	time.Sleep(20 * time.Millisecond)
	if fails.Load() != 1 {
		t.Fatalf("失败回调应恰一次: %d", fails.Load())
	}
	if got.EventKey != "e" || got.Severity != "warning" || got.ErrorClass != "http_status_500" {
		t.Fatalf("失败事实不符: %+v", got)
	}

	var oks atomic.Int32
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	t.Cleanup(srv2.Close)
	n2 := New(Options{WebhookURL: srv2.URL, Backoff: []time.Duration{time.Millisecond},
		Logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		OnDeliverySuccess: func(eventKey string, at time.Time) { oks.Add(1) }})
	n2.Emit("ok", "info", "s")
	waitFor(t, func() bool { return oks.Load() == 1 })
}

func TestAlerting_回调panic不反噬投递(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
	}))
	t.Cleanup(srv.Close)
	n := New(Options{WebhookURL: srv.URL, Backoff: []time.Duration{time.Millisecond},
		Logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		OnDeliverySuccess: func(string, time.Time) { panic("boom") }})
	n.Emit("p", "info", "s")
	waitFor(t, func() bool { return hits.Load() >= 1 }) // 投递完成即证明回调 panic 未反噬
}
