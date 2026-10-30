// 上游健康端点三态契约:未启用空态、已启用零探针结果、降级相位透出。
package admin_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/solvela/muniu/internal/admin"
	"github.com/solvela/muniu/internal/health"
	"github.com/solvela/muniu/internal/testkit"
)

func upstreamHealth(t *testing.T, h http.Handler) map[string]any {
	t.Helper()
	code, body := call(t, h, "GET", "/admin/v1/upstream-health", "t", "")
	if code != 200 {
		t.Fatalf("status=%d body=%s", code, body)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("响应非 JSON: %s", body)
	}
	return out
}

func TestAdminUpstreamHealth_三态(t *testing.T) {
	// 未装配:enabled=false 空态
	h := admin.New(admin.Deps{DB: openDB(t), Token: "t",
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	out := upstreamHealth(t, h)
	if out["enabled"] != false {
		t.Fatalf("未装配应为空态: %v", out)
	}

	// 已启用、尚无状态(未 Start):enabled=true 空集
	mgr := health.NewManager(health.Options{
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Sources: func() map[string]health.Channel { return map[string]health.Channel{} },
	})
	h2 := admin.New(admin.Deps{DB: openDB(t), Token: "t", Health: mgr,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	out = upstreamHealth(t, h2)
	if out["enabled"] != true {
		t.Fatalf("已启用应 enabled=true: %v", out)
	}

	// 降级相位透出:免费探针指向坏上游,单阈值即降级
	upBad := testkit.StartJSONUpstream(t, 503, `{}`)
	mgr2 := health.NewManager(health.Options{
		FailureThreshold: 1,
		Logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		Sources: func() map[string]health.Channel {
			return map[string]health.Channel{
				"prov-a": {Name: "prov-a", Adapter: "openai", BaseURL: upBad.URL(), FreeProbe: true,
					Keys: []health.ChannelKey{{ID: "K", Value: "sk"}}},
			}
		},
	})
	mgr2.Start()
	t.Cleanup(mgr2.Close)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && !mgr2.Contracted("prov-a") {
		time.Sleep(10 * time.Millisecond)
	}
	h3 := admin.New(admin.Deps{DB: openDB(t), Token: "t", Health: mgr2,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	out = upstreamHealth(t, h3)
	providers, _ := out["providers"].([]any)
	found := false
	for _, p := range providers {
		pv, _ := p.(map[string]any)
		if pv["provider"] == "prov-a" {
			found = true
			if pv["phase"] != "degraded" || pv["reason"] != "control_plane" {
				t.Fatalf("降级相位/原因不符: %v", pv)
			}
		}
	}
	if !found {
		t.Fatalf("快照应含 prov-a: %v", out)
	}
}
