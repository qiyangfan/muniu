// Manager 级回归契约:探针 URL 尾斜杠归一、不支持探针的适配器静默跳过、
// 无 key 渠道被动降级到期转半开、移除渠道状态过期清理、管理面探针配置透出。
package health

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// waitFor 轮询等待条件达成(Manager 循环异步,断言须等待)。
func waitFor(t *testing.T, cond func() bool) {
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

// countingUpstream 计数上游:记录命中数与最近请求路径。
type countingUpstream struct {
	srv      *httptest.Server
	hits     atomic.Int64
	lastPath atomic.Value
}

func newCountingUpstream() *countingUpstream {
	cu := &countingUpstream{}
	cu.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cu.hits.Add(1)
		cu.lastPath.Store(r.URL.Path)
		w.WriteHeader(200)
	}))
	return cu
}

// TestManager_探针URL尾斜杠归一 尾斜杠 base_url 在数据面被 adapter 层归一,
// 探针面必须同口径,否则拼出 //models 404 误熔断健康渠道。
func TestManager_探针URL尾斜杠归一(t *testing.T) {
	up := newCountingUpstream()
	defer up.srv.Close()
	m := NewManager(Options{
		Interval: 20 * time.Millisecond, Timeout: time.Second,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Sources: func() map[string]Channel {
			return map[string]Channel{"p": {
				Name: "p", Adapter: "openai", BaseURL: up.srv.URL + "/", FreeProbe: true,
				Keys: []ChannelKey{{ID: "k", Value: "v"}},
			}}
		},
	})
	m.Start()
	defer m.Close()
	waitFor(t, func() bool {
		providers, _ := m.Snapshot()
		return len(providers) == 1 && providers[0].LastProbeOK != nil && *providers[0].LastProbeOK
	})
	if got := up.lastPath.Load().(string); got != "/models" {
		t.Fatalf("探针路径应归一为 /models,实际 %q", got)
	}
}

// TestManager_不支持探针的适配器静默跳过 gemini 原生 v1 无探针端点(登记限制):
// 声明探针参数不得转化为每轮失败计数(否则误熔断健康渠道),应静默跳过、被动统计照常。
func TestManager_不支持探针的适配器静默跳过(t *testing.T) {
	up := newCountingUpstream()
	defer up.srv.Close()
	m := NewManager(Options{
		Interval: 20 * time.Millisecond, Timeout: time.Second,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Sources: func() map[string]Channel {
			return map[string]Channel{"g": {
				Name: "g", Adapter: "gemini", BaseURL: up.srv.URL,
				FreeProbe: true, ProbeModel: "gemini-pro",
				Keys: []ChannelKey{{ID: "k", Value: "v"}},
			}}
		},
	})
	m.Start()
	defer m.Close()
	// 5+ 个 tick 周期后:零探针请求、零探针结果(闸在 tick,任一层都不发)。
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if hits := up.hits.Load(); hits != 0 {
		t.Fatalf("不支持探针的适配器不得发起探针请求,实际 %d 次", hits)
	}
	providers, _ := m.Snapshot()
	if len(providers) != 1 || providers[0].LastProbeOK != nil {
		t.Fatalf("应无探针结果: %+v", providers)
	}
	// 管理面透出装配后的探针声明(spec:端点返回探针配置)。
	if !providers[0].FreeProbe || providers[0].ProbeModel != "gemini-pro" {
		t.Fatalf("探针配置应如实透出: %+v", providers[0])
	}
}

// TestManager_无key渠道被动降级到期转半开 无 key 渠道零探针载体,但状态推进(open 计时到期
// 转半开)依赖周期 tick 触达——排除在 Sources 外会让被动降级永不试探恢复(永久熔断)。
func TestManager_无key渠道被动降级到期转半开(t *testing.T) {
	m := NewManager(Options{
		Interval: 20 * time.Millisecond, Timeout: time.Second,
		OpenFor: 40 * time.Millisecond, // 熔断计时极短,到期即转半开
		// 被动窗口:3 样本即判越线(全部失败)
		SwitchWindow: time.Minute, SwitchMinSamples: 3, SwitchRateMilli: 1000,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Sources: func() map[string]Channel {
			return map[string]Channel{"k": {Name: "k", Adapter: "openai", BaseURL: "http://127.0.0.1:1", FreeProbe: false}}
		},
	})
	m.Start()
	defer m.Close()
	for i := 0; i < 5; i++ {
		m.ProviderTraffic("k", true)
	}
	waitFor(t, func() bool { return m.Contracted("k") }) // 被动降级
	waitFor(t, func() bool {
		providers, _ := m.Snapshot()
		return len(providers) == 1 && providers[0].Phase == "half_open" && !m.Contracted("k")
	})
}

// TestManager_移除渠道状态过期清理 热替换移除的渠道不再有信号触达,
// 超期(10×间隔)后状态与被动窗口一并清理,防幽灵渠道永驻管理面。
func TestManager_移除渠道状态过期清理(t *testing.T) {
	present := atomic.Bool{}
	present.Store(true)
	m := NewManager(Options{
		Interval: 20 * time.Millisecond, Timeout: time.Second,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Sources: func() map[string]Channel {
			if !present.Load() {
				return map[string]Channel{}
			}
			return map[string]Channel{"gone": {Name: "gone", Adapter: "openai", BaseURL: "http://127.0.0.1:1", FreeProbe: false}}
		},
	})
	m.Start()
	defer m.Close()
	m.ProviderTraffic("gone", false) // 触达建态
	waitFor(t, func() bool {
		providers, _ := m.Snapshot()
		return len(providers) == 1
	})
	present.Store(false)
	waitFor(t, func() bool {
		providers, _ := m.Snapshot()
		return len(providers) == 0
	})
}
