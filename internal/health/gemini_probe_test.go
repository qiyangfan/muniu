package health

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// TestProbeSupported_GeminiFreeOnly gemini 原生具备免费控制面探针（GET /models），
// 但推理探针仍不支持；两者必须是独立能力位，防止 probe_model 误走 OpenAI 形态。
func TestProbeSupported_GeminiFreeOnly(t *testing.T) {
	if !ProbeSupported("gemini") {
		t.Fatal("gemini 应支持免费控制面探针")
	}
	if InferenceProbeSupported("gemini") {
		t.Fatal("gemini 不应支持推理探针")
	}
	if !ProbeSupported("openai") || !InferenceProbeSupported("openai") {
		t.Fatal("openai 应同时支持免费与推理探针")
	}
	if ProbeSupported("unknown-adapter") || InferenceProbeSupported("unknown-adapter") {
		t.Fatal("未登记适配器不得声称支持探针")
	}
}

// TestManager_Gemini免费探针鉴权与路径 Gemini 探针必须走 models.list 路径，
// 使用 x-goog-api-key，且不得混入 Bearer/Anthropic 鉴权头。
func TestManager_Gemini免费探针鉴权与路径(t *testing.T) {
	var gotPath, gotAPIKey, gotAuth atomic.Value
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath.Store(r.URL.Path)
		gotAPIKey.Store(r.Header.Get("x-goog-api-key"))
		gotAuth.Store(r.Header.Get("Authorization"))
		w.WriteHeader(200)
	}))
	defer up.Close()
	m := NewManager(context.Background(), Options{
		Interval: 20 * time.Millisecond, Timeout: time.Second,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Sources: func() map[string]Channel {
			return map[string]Channel{"g": {
				Name: "g", Adapter: "gemini", BaseURL: up.URL, FreeProbe: true,
				Keys: []ChannelKey{{ID: "k", Value: "gk-1"}},
			}}
		},
	})
	m.Start()
	defer m.Close()
	waitFor(t, func() bool {
		providers, _ := m.Snapshot()
		return len(providers) == 1 && providers[0].LastProbeOK != nil && *providers[0].LastProbeOK
	})
	if got := gotPath.Load().(string); got != "/models" {
		t.Fatalf("gemini 探针路径应为 /models,实际 %q", got)
	}
	if got := gotAPIKey.Load().(string); got != "gk-1" {
		t.Fatalf("gemini 探针应带 x-goog-api-key,实际 %q", got)
	}
	if got, _ := gotAuth.Load().(string); got != "" {
		t.Fatalf("gemini 探针不得携带 Authorization,实际 %q", got)
	}
}

// TestManager_Gemini不发起推理探针 probe_model 在 gemini 上既不能生效也不能
// 被当成 OpenAI 形态发出去；tick 必须以推理能力位为闸。
func TestManager_Gemini不发起推理探针(t *testing.T) {
	up := newCountingUpstream()
	defer up.srv.Close()
	m := NewManager(context.Background(), Options{
		Interval: 20 * time.Millisecond, Timeout: time.Second,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Sources: func() map[string]Channel {
			return map[string]Channel{"g": {
				Name: "g", Adapter: "gemini", BaseURL: up.srv.URL,
				FreeProbe: false, ProbeModel: "gemini-pro",
				Keys: []ChannelKey{{ID: "k", Value: "gk-1"}},
			}}
		},
	})
	m.Start()
	defer m.Close()
	deadline := time.Now().Add(150 * time.Millisecond)
	for time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if hits := up.hits.Load(); hits != 0 {
		t.Fatalf("gemini 不得发起推理探针,实际 %d 次", hits)
	}
}
