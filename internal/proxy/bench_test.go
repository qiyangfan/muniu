// BenchmarkSSEForward 测量 1000 个 SSE 事件的端到端转发开销。
package proxy

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/solvela/muniu/internal/adapter"
	"github.com/solvela/muniu/internal/config"
	"github.com/solvela/muniu/internal/keypool"
	"github.com/solvela/muniu/internal/testkit"
)

// BenchmarkSSEForward 单请求 1000 事件流式转发的端到端开销，含 HTTP 往返与 fake 上游。
func BenchmarkSSEForward(b *testing.B) {
	const chunkCount = 1000
	payload := `{"id":"bench","choices":[{"index":0,"delta":{"content":"x"}}]}`

	events := make([]testkit.Event, 0, chunkCount+1)
	for i := 0; i < chunkCount; i++ {
		events = append(events, testkit.Event{Data: "data: " + payload + "\n\n"})
	}
	events = append(events, testkit.Event{Data: "data: [DONE]\n\n"})

	up := testkit.StartStreamUpstream(b, events)
	reg := adapter.NewRegistry()
	reg.Register("fake", adapter.NewOpenAI(up.URL(), "sk-bench"))
	p := New(Config{
		Registry: reg,
		Routes: map[string][]config.Candidate{
			"alias-a": {{Provider: "fake", Model: "upstream-a", Priority: 0}},
		},
		Timeouts: Timeouts{Dial: benchTimeout, FirstToken: benchTimeout, ChunkIdle: benchTimeout},
		Pools: map[string]*keypool.Pool{
			"fake": keypool.NewPool([]keypool.KeyInput{{ID: "K", Value: "sk-bench", Weight: 1}}),
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ts := httptest.NewServer(p.Handler())
	defer ts.Close()

	body := `{"model":"alias-a","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json", strings.NewReader(body))
		if err != nil {
			b.Fatal(err)
		}
		if _, err := io.Copy(io.Discard, resp.Body); err != nil {
			b.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			b.Fatalf("状态码 %d", resp.StatusCode)
		}
	}
}

// benchTimeout 基准用超时上限，不应触发。
const benchTimeout = 30e9
