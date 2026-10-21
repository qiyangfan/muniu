// 验证 Responses 入站翻译、流式事件、危险字段拒绝和 usage 计量。
package proxy

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/solvela/muniu/internal/adapter"
	"github.com/solvela/muniu/internal/config"
	"github.com/solvela/muniu/internal/keypool"
	"github.com/solvela/muniu/internal/metering"
	"github.com/solvela/muniu/internal/store"
	"github.com/solvela/muniu/internal/testkit"
)

func newMeteredProxyForResponses(t *testing.T, jsonBody string) *httptest.Server {
	t.Helper()
	up := testkit.StartJSONUpstream(t, 200, jsonBody)
	reg := adapter.NewRegistry()
	reg.Register("prov-a", adapter.NewOpenAI(up.URL(), ""))
	db, err := store.Open(t.TempDir() + "/r.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	pipeline := metering.NewPipeline(db, metering.Options{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	p := New(Config{
		Registry: reg,
		Routes: map[string][]config.Candidate{
			"alias-a": {{Provider: "prov-a", Model: "up-a", Priority: 0}},
		},
		Timeouts: Timeouts{Dial: time.Second, FirstToken: 10 * time.Second, ChunkIdle: 10 * time.Second},
		Pipeline: pipeline,
		Pools:    map[string]*keypool.Pool{"prov-a": keypool.NewPool([]keypool.KeyInput{{ID: "KA", Value: "k", Weight: 1}})},
		RandIntn: func(n int) int { return 0 },
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ts := httptest.NewServer(p.HandlerResponses())
	t.Cleanup(ts.Close)
	return ts
}

const usageJSONBody = `{"choices":[{"index":0,"message":{"role":"assistant","content":"你好"},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":8}}`

// 验证等价的 Responses 和 Chat 请求会生成相同的上游请求体。
func TestResponsesE2E_与等价chat请求字节同构(t *testing.T) {
	up := testkit.StartJSONUpstream(t, 200, usageJSONBody)
	reg := adapter.NewRegistry()
	reg.Register("prov-a", adapter.NewOpenAI(up.URL(), ""))
	p := New(Config{
		Registry: reg,
		Routes: map[string][]config.Candidate{
			"alias-a": {{Provider: "prov-a", Model: "up-a", Priority: 0}},
		},
		Timeouts: Timeouts{Dial: time.Second, FirstToken: 10 * time.Second, ChunkIdle: 10 * time.Second},
		Pools:    map[string]*keypool.Pool{"prov-a": keypool.NewPool([]keypool.KeyInput{{ID: "KA", Value: "k", Weight: 1}})},
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ts := httptest.NewServer(p.HandlerResponses())
	chatTS := httptest.NewServer(p.Handler())
	t.Cleanup(func() { ts.Close(); chatTS.Close() })

	respBody := `{"model":"alias-a","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],"stream":false}`
	resp, body := postChat(t, ts.URL, respBody)
	if resp.StatusCode != 200 {
		t.Fatalf("responses 应 200: %d %s", resp.StatusCode, body)
	}
	chatUp1 := up.LastBody()
	if _, body := postChat(t, chatTS.URL, `{"model":"alias-a","messages":[{"role":"user","content":"hi"}]}`); true {
		_ = body
	}
	chatUp2 := up.LastBody()
	if string(chatUp1) != string(chatUp2) {
		t.Fatalf("canonical 不等价:\n responses=%s\n chat=%s", chatUp1, chatUp2)
	}
}

// 非流式：客户端收到 Responses 对象，含 message/output_text/usage。
func TestResponsesE2E_非流式对象与usage(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle("POST /v1/chat/completions", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(usageJSONBody))
	}))
	mux.Handle("POST /v1/responses", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(usageJSONBody))
	}))
	_ = mux
	ts := newMeteredProxyForResponses(t, usageJSONBody)
	resp, body := postChat(t, ts.URL, `{"model":"alias-a","input":"hi","stream":false}`)
	if resp.StatusCode != 200 {
		t.Fatalf("应 200: %d %s", resp.StatusCode, body)
	}
	var obj struct {
		Object string `json:"object"`
		Status string `json:"status"`
		Output []struct {
			Type    string `json:"type"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
		Usage struct {
			InputTokens  int64 `json:"input_tokens"`
			OutputTokens int64 `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(body), &obj); err != nil {
		t.Fatalf("非 OpenAI 形态: %v (%s)", err, body)
	}
	if obj.Object != "response" || obj.Status != "completed" {
		t.Fatalf("对象形态不符: %s", body)
	}
	if len(obj.Output) != 1 || obj.Output[0].Content[0].Text != "你好" {
		t.Fatalf("output 不符: %s", body)
	}
	if obj.Usage.InputTokens != 12 || obj.Usage.OutputTokens != 8 {
		t.Fatalf("usage 不符: %s", body)
	}
}

// 流式：事件顺序不变式；chat 的 [DONE] 不得透传，Responses 以 completed 收尾。
func TestResponsesE2E_流式事件序列(t *testing.T) {
	up := testkit.StartStreamUpstream(t, testkit.NormalEvents())
	reg := adapter.NewRegistry()
	reg.Register("prov-a", adapter.NewOpenAI(up.URL(), ""))
	p := New(Config{
		Registry: reg,
		Routes: map[string][]config.Candidate{
			"alias-a": {{Provider: "prov-a", Model: "up-a", Priority: 0}},
		},
		Timeouts: Timeouts{Dial: time.Second, FirstToken: 10 * time.Second, ChunkIdle: 10 * time.Second},
		Pools:    map[string]*keypool.Pool{"prov-a": keypool.NewPool([]keypool.KeyInput{{ID: "KA", Value: "k", Weight: 1}})},
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ts := httptest.NewServer(p.HandlerResponses())
	t.Cleanup(ts.Close)

	resp, body := postChat(t, ts.URL, `{"model":"alias-a","input":"hi","stream":true}`)
	if resp.StatusCode != 200 {
		t.Fatalf("应 200: %d", resp.StatusCode)
	}
	if strings.Contains(body, "data: [DONE]") {
		t.Fatal("[DONE] 不得透传(Responses 以 response.completed 收尾)")
	}
	types := []string{}
	for _, line := range strings.Split(body, "\n\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var ev struct {
			Type string `json:"type"`
		}
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev) == nil && ev.Type != "" {
			types = append(types, ev.Type)
		}
	}
	if len(types) < 4 || types[0] != "response.created" || types[len(types)-1] != "response.completed" {
		t.Fatalf("序列不变式破坏: %v", types)
	}
	for i, ty := range types {
		if strings.HasSuffix(ty, ".delta") && (i == 0 || i == len(types)-1) {
			t.Fatalf("delta 不得为首/终帧: %v", types)
		}
	}
	if !strings.Contains(body, `"input_tokens":12`) {
		t.Fatalf("completed usage 缺失: %s", body)
	}
}

// 危险字段：previous_response_id 在入口 400 拒绝，不触达上游。
func TestResponsesE2E_危险字段拒绝(t *testing.T) {
	up := testkit.StartJSONUpstream(t, 200, usageJSONBody)
	reg := adapter.NewRegistry()
	reg.Register("prov-a", adapter.NewOpenAI(up.URL(), ""))
	p := New(Config{
		Registry: reg,
		Routes: map[string][]config.Candidate{
			"alias-a": {{Provider: "prov-a", Model: "up-a", Priority: 0}},
		},
		Pools:  map[string]*keypool.Pool{"prov-a": keypool.NewPool([]keypool.KeyInput{{ID: "KA", Value: "k", Weight: 1}})},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ts := httptest.NewServer(p.HandlerResponses())
	t.Cleanup(ts.Close)

	resp, body := postChat(t, ts.URL, `{"model":"alias-a","previous_response_id":"resp_x","input":"hi"}`)
	if resp.StatusCode != 400 {
		t.Fatalf("应 400: %d %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "previous_response_id_unsupported") {
		t.Fatalf("信封码不符: %s", body)
	}
	if n := up.RequestCount(); n != 0 {
		t.Fatalf("拒绝不得触达上游: %d", n)
	}
}
