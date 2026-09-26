// Anthropic Messages 入站端到端钉子(ADR-0013):canonical 等价性(翻译产物 ≡ 等价 chat
// 请求字节,传递性钉死治理/缓存/计量同构)、非流式 message 对象改写、流式事件顺序不变式
// ([DONE] 不透传)、不支持内容块 fail-fast 不触达上游、x-api-key 鉴权头适配。
package proxy

import (
	"encoding/json"
	"io"
	"log/slog"
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

func newMeteredProxyForMessages(t *testing.T, jsonBody string) *httptest.Server {
	t.Helper()
	up := testkit.StartJSONUpstream(t, 200, jsonBody)
	reg := adapter.NewRegistry()
	reg.Register("prov-a", adapter.NewOpenAI(up.URL(), ""))
	db, err := store.Open(t.TempDir() + "/m.db")
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
	ts := httptest.NewServer(p.HandlerMessages())
	t.Cleanup(ts.Close)
	return ts
}

// 等价性钉子:Messages 请求与手工等价 chat 请求,到达上游的字节逐字节一致
// (canonical 同构 → 治理/缓存/计量行为等价的传递性证明)。
func TestMessagesE2E_与等价chat请求字节同构(t *testing.T) {
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
	ts := httptest.NewServer(p.HandlerMessages())
	chatTS := httptest.NewServer(p.Handler())
	t.Cleanup(func() { ts.Close(); chatTS.Close() })

	respBody := `{"model":"alias-a","max_tokens":1024,"messages":[{"role":"user","content":"hi"}],"stream":false}`
	resp, body := postChat(t, ts.URL, respBody)
	if resp.StatusCode != 200 {
		t.Fatalf("messages 应 200: %d %s", resp.StatusCode, body)
	}
	msgUp1 := up.LastBody()
	if _, body := postChat(t, chatTS.URL, `{"model":"alias-a","messages":[{"role":"user","content":"hi"}],"max_tokens":1024}`); true {
		_ = body
	}
	msgUp2 := up.LastBody()
	if string(msgUp1) != string(msgUp2) {
		t.Fatalf("canonical 不等价:\n messages=%s\n chat=%s", msgUp1, msgUp2)
	}
}

// 非流式:客户端收到 Anthropic message 对象(thinking/text/tool_use、stop_reason、usage)。
func TestMessagesE2E_非流式对象与usage(t *testing.T) {
	ts := newMeteredProxyForMessages(t, usageJSONBody)
	resp, body := postChat(t, ts.URL, `{"model":"alias-a","max_tokens":100,"messages":[{"role":"user","content":"你好"}]}`)
	if resp.StatusCode != 200 {
		t.Fatalf("应 200: %d %s", resp.StatusCode, body)
	}
	var obj struct {
		Type       string  `json:"type"`
		Role       string  `json:"role"`
		StopReason *string `json:"stop_reason"`
		Content    []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Usage struct {
			InputTokens  int64 `json:"input_tokens"`
			OutputTokens int64 `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(body), &obj); err != nil {
		t.Fatalf("非 Anthropic 形态: %v (%s)", err, body)
	}
	if obj.Type != "message" || obj.Role != "assistant" {
		t.Fatalf("对象形态不符: %s", body)
	}
	if obj.StopReason == nil || *obj.StopReason != "end_turn" {
		t.Fatalf("stop_reason 应为 end_turn: %s", body)
	}
	if len(obj.Content) != 1 || obj.Content[0].Type != "text" || obj.Content[0].Text != "你好" {
		t.Fatalf("content 不符: %s", body)
	}
	if obj.Usage.InputTokens != 12 || obj.Usage.OutputTokens != 8 {
		t.Fatalf("usage 不符: %s", body)
	}
}

// 流式:事件顺序不变式(message_start 首帧/message_stop 终帧);chat 的 [DONE] 不得透传。
func TestMessagesE2E_流式事件序列(t *testing.T) {
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
	ts := httptest.NewServer(p.HandlerMessages())
	t.Cleanup(ts.Close)

	resp, body := postChat(t, ts.URL, `{"model":"alias-a","max_tokens":100,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if resp.StatusCode != 200 {
		t.Fatalf("应 200: %d", resp.StatusCode)
	}
	if strings.Contains(body, "data: [DONE]") {
		t.Fatal("[DONE] 不得透传(Messages 以 message_stop 收尾)")
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
	if len(types) < 4 || types[0] != "message_start" || types[len(types)-1] != "message_stop" {
		t.Fatalf("序列不变式破坏: %v", types)
	}
	if !strings.Contains(body, `"text_delta"`) {
		t.Fatalf("缺文本增量事件: %s", body)
	}
	if !strings.Contains(body, `"stop_reason":"end_turn"`) {
		t.Fatalf("message_delta 缺 stop_reason: %s", body)
	}
}

// 不支持内容块:image 在入口 400(Anthropic error JSON),不触达上游。
func TestMessagesE2E_不支持内容块拒绝(t *testing.T) {
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
	ts := httptest.NewServer(p.HandlerMessages())
	t.Cleanup(ts.Close)

	resp, body := postChat(t, ts.URL, `{"model":"alias-a","max_tokens":10,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64"}}]}]}`)
	if resp.StatusCode != 400 {
		t.Fatalf("应 400: %d %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, `"type":"error"`) || !strings.Contains(body, `invalid_request_error`) {
		t.Fatalf("错误形态不符: %s", body)
	}
	if n := up.RequestCount(); n != 0 {
		t.Fatalf("拒绝不得触达上游: %d", n)
	}
}
