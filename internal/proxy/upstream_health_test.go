// 上游健康熔断集成契约:降级渠道候选域收缩(零上游调用)、多候选透明切换、
// 单候选快速失败 503 provider_circuit_open、被动统计与换道率计数、降级/恢复事件发射。
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
	"github.com/solvela/muniu/internal/health"
	"github.com/solvela/muniu/internal/keypool"
	"github.com/solvela/muniu/internal/testkit"
)

func waitForCond(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("条件未在时限内达成")
}

// healthHarness 健康熔断测试台:prov-a(free 探针指向 baseURLA)、prov-b(恒 200)。
type healthHarness struct {
	ts       *httptest.Server
	upA      *testkit.Upstream
	upB      *testkit.Upstream
	mgr      *health.Manager
	events   chan health.Event
	baseURLA atomic.Value // string:可切换 prov-a 探针目标(降级→恢复剧本)
}

func newHealthHarness(t *testing.T, statusA int) *healthHarness {
	t.Helper()
	upA := testkit.StartJSONUpstream(t, statusA, `{"error":"upstream down"}`)
	upB := testkit.StartJSONUpstream(t, 200, `{"choices":[{"message":{"role":"assistant","content":"ok-b"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	reg := adapter.NewRegistry()
	reg.Register("prov-a", adapter.NewOpenAI(upA.URL(), ""))
	reg.Register("prov-b", adapter.NewOpenAI(upB.URL(), ""))
	poolA := keypool.NewPool([]keypool.KeyInput{{ID: "KA", Value: "sk-a", Weight: 1}})
	poolB := keypool.NewPool([]keypool.KeyInput{{ID: "KB", Value: "sk-b", Weight: 1}})

	h := &healthHarness{upA: upA, upB: upB, events: make(chan health.Event, 16)}
	h.baseURLA.Store(upA.URL())
	mgr := health.NewManager(health.Options{
		FailureThreshold: 1, // 探针单败即降级(测试加速)
		Interval:         20 * time.Millisecond,
		Logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		Sources: func() map[string]health.Channel {
			return map[string]health.Channel{
				"prov-a": {
					Name: "prov-a", Adapter: "openai",
					BaseURL:   h.baseURLA.Load().(string), // 探针目标可切换
					FreeProbe: true,
					Keys:      []health.ChannelKey{{ID: "KA", Value: "sk-a"}},
				},
			}
		},
		Emit: func(e health.Event) { h.events <- e },
	})
	mgr.Start()
	t.Cleanup(mgr.Close)

	p := New(Config{
		Registry: reg,
		Routes: map[string][]config.Candidate{
			"alias-a": {
				{Provider: "prov-a", Model: "up-a", Priority: 0, Weight: 1},
				{Provider: "prov-b", Model: "up-b", Priority: 1, Weight: 1},
			},
		},
		Timeouts: Timeouts{Dial: time.Second, FirstToken: 10 * time.Second, ChunkIdle: 10 * time.Second},
		Pools:    map[string]*keypool.Pool{"prov-a": poolA, "prov-b": poolB},
		Health:   mgr,
		RandIntn: func(n int) int { return 0 },
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	h.ts = httptest.NewServer(p.Handler())
	t.Cleanup(h.ts.Close)
	h.mgr = mgr
	return h
}

func (h *healthHarness) post(t *testing.T) (int, string) {
	t.Helper()
	resp, err := http.Post(h.ts.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(`{"model":"alias-a","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode, string(b)
}

func TestUpstreamHealth_降级收缩与透明切换(t *testing.T) {
	h := newHealthHarness(t, 503)
	waitForCond(t, func() bool { return h.mgr.Contracted("prov-a") })

	// 双候选:prov-a 收缩,请求透明切到 prov-b 成功;prov-a 零上游调用
	// (探针自身会打 prov-a,以收缩建立后的计数为基线)
	hitsBefore := h.upA.RequestCount()
	code, body := h.post(t)
	if code != 200 || !strings.Contains(body, "ok-b") {
		t.Fatalf("收缩后应透明切换到 prov-b: code=%d body=%s", code, body)
	}
	if h.upA.RequestCount() != hitsBefore {
		t.Fatalf("收缩渠道不得因请求产生上游调用: +%d", h.upA.RequestCount()-hitsBefore)
	}

	// 被动统计与换道率:收缩跳过不计换道(spec),prov-b 成功计入窗口
	providers, aliases := h.mgr.Snapshot()
	foundB := false
	for _, pv := range providers {
		if pv.Provider == "prov-a" && pv.Phase == "degraded" {
			foundB = true
		}
	}
	if !foundB {
		t.Fatalf("快照应含 prov-a degraded 相位: %+v", providers)
	}
	samples := 0
	for _, av := range aliases {
		if av.Alias == "alias-a" {
			samples = av.Samples
			if av.Breached {
				t.Fatalf("收缩跳过不计换道,不得越线: %+v", av)
			}
		}
	}
	if samples < 1 {
		t.Fatalf("别名换道率分母应计数: %+v", aliases)
	}

	// 降级事件已发射
	select {
	case ev := <-h.events:
		if ev.Kind != "provider_degraded" || ev.Provider != "prov-a" {
			t.Fatalf("降级事件不符: %+v", ev)
		}
	default:
		t.Fatal("应收到 provider_degraded 事件")
	}
}

func TestUpstreamHealth_单候选熔断503快速失败(t *testing.T) {
	upA := testkit.StartJSONUpstream(t, 503, `{"error":"down"}`)
	reg := adapter.NewRegistry()
	reg.Register("prov-a", adapter.NewOpenAI(upA.URL(), ""))
	poolA := keypool.NewPool([]keypool.KeyInput{{ID: "KA", Value: "sk-a", Weight: 1}})
	mgr := health.NewManager(health.Options{
		FailureThreshold: 1, Interval: time.Hour,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Sources: func() map[string]health.Channel {
			return map[string]health.Channel{
				"prov-a": {Name: "prov-a", Adapter: "openai", BaseURL: upA.URL(), FreeProbe: true,
					Keys: []health.ChannelKey{{ID: "KA", Value: "sk-a"}}},
			}
		},
	})
	mgr.Start()
	t.Cleanup(mgr.Close)
	waitForCond(t, func() bool { return mgr.Contracted("prov-a") })
	hitsBefore := upA.RequestCount()

	p := New(Config{
		Registry: reg,
		Routes: map[string][]config.Candidate{
			"alias-a": {{Provider: "prov-a", Model: "up-a", Priority: 0, Weight: 1}},
		},
		Timeouts: Timeouts{Dial: time.Second, FirstToken: 10 * time.Second, ChunkIdle: 10 * time.Second},
		Pools:    map[string]*keypool.Pool{"prov-a": poolA},
		Health:   mgr,
		RandIntn: func(n int) int { return 0 },
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ts := httptest.NewServer(p.Handler())
	t.Cleanup(ts.Close)
	resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(`{"model":"alias-a","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 503 || !strings.Contains(string(b), "provider_circuit_open") {
		t.Fatalf("单候选熔断应 503 provider_circuit_open: code=%d body=%s", resp.StatusCode, string(b))
	}
	if upA.RequestCount() != hitsBefore {
		t.Fatalf("快速失败不得产生上游调用: %d", upA.RequestCount()-hitsBefore)
	}
}

func TestUpstreamHealth_探针恢复与恢复事件(t *testing.T) {
	h := newHealthHarness(t, 503)
	waitForCond(t, func() bool { return h.mgr.Contracted("prov-a") })
	// 探针目标切到健康上游(与 prov-b 同源):下轮免费探针成功,控制面单阈值(1)恢复
	h.baseURLA.Store(h.upB.URL())
	waitForCond(t, func() bool { return !h.mgr.Contracted("prov-a") })
	// 恢复事件已发射
	deadline := time.Now().Add(3 * time.Second)
	for {
		select {
		case ev := <-h.events:
			if ev.Kind == "provider_recovered" && ev.Provider == "prov-a" {
				return
			}
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("应收到 provider_recovered 事件")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
