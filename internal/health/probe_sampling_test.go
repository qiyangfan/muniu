package health

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// TestManager_探针轮换取样 多渠道 key 必须轮换,不能固定首键。
func TestManager_探针轮换取样(t *testing.T) {
	var mu sync.Mutex
	var auths []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auths = append(auths, r.Header.Get("Authorization"))
		mu.Unlock()
		w.WriteHeader(200)
	}))
	defer up.Close()
	m := NewManager(context.Background(), Options{
		Interval: 20 * time.Millisecond, Timeout: time.Second,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Sources: func() map[string]Channel {
			return map[string]Channel{"p": {
				Name: "p", Adapter: "openai", BaseURL: up.URL, FreeProbe: true,
				Keys: []ChannelKey{{ID: "k1", Value: "v1"}, {ID: "k2", Value: "v2"}},
			}}
		},
	})
	m.Start()
	defer m.Close()
	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(auths) >= 2
	})
	mu.Lock()
	got := append([]string(nil), auths[:2]...)
	mu.Unlock()
	if got[0] == got[1] {
		t.Fatalf("连续两轮探针应轮换 key,实际 %v", got)
	}
}

// TestManager_全不可用跳过探针 无可用 key 时必须跳过本轮,不计失败、不降级。
func TestManager_全不可用跳过探针(t *testing.T) {
	up := newCountingUpstream()
	defer up.srv.Close()
	m := NewManager(context.Background(), Options{
		Interval: 20 * time.Millisecond, Timeout: time.Second,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Sources: func() map[string]Channel {
			return map[string]Channel{"p": {
				Name: "p", Adapter: "openai", BaseURL: up.srv.URL, FreeProbe: true,
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
		t.Fatalf("无可用 key 不得发起探针,实际 %d 次", hits)
	}
	providers, _ := m.Snapshot()
	if len(providers) != 1 || providers[0].LastProbeOK != nil || providers[0].Phase != "healthy" {
		t.Fatalf("应跳过探针且保持 healthy: %+v", providers)
	}
}
