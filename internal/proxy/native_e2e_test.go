// 验证原生协议直通的请求保真、流式计量、换道、治理、缓存和状态路由。
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
	"github.com/solvela/muniu/internal/adapter/bridge"
	"github.com/solvela/muniu/internal/config"
	"github.com/solvela/muniu/internal/keypool"
	"github.com/solvela/muniu/internal/testkit"
)

// anthropic 非流式响应，usage 为真实形态。
const anthropicJSONBody = `{"id":"msg_1","type":"message","role":"assistant","model":"claude-up","content":[{"type":"text","text":"看图回答"}],"stop_reason":"end_turn","usage":{"input_tokens":21,"output_tokens":5}}`

// anthropic 流式事件，message_start 携真实 input_tokens=21，翻译路径登记偏差为恒 0。
func anthropicStreamEvents() []testkit.Event {
	return []testkit.Event{
		{Data: "data: " + `{"type":"message_start","message":{"id":"msg_1","usage":{"input_tokens":21}}}` + "\n\n"},
		{Data: "data: " + `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n"},
		{Data: "data: " + `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"你好"}}` + "\n\n"},
		{Data: "data: " + `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}` + "\n\n"},
		{Data: "data: " + `{"type":"message_stop"}` + "\n\n"},
	}
}

// newNativeProxy 直通拓扑：anthropic 桥接加 openai 系候选，后者可声明 native_responses。
type nativeTopo struct {
	messagesUp *testkit.Upstream
	openaiUp   *testkit.Upstream
	p          *Proxy
}

func newNativeProxy(t *testing.T, msgsUp, openaiUp *testkit.Upstream, declaredNative bool, caps []config.ModelCapability) *nativeTopo {
	t.Helper()
	reg := adapter.NewRegistry()
	reg.Register("anth", bridge.NewAnthropic(msgsUp.URL(), adapter.Options{}))
	reg.Register("oai", adapter.NewOpenAI(openaiUp.URL(), ""))
	p := New(Config{
		Registry: reg,
		Providers: []config.Provider{
			{Name: "anth", Adapter: "anthropic"},
			{Name: "oai", Adapter: "openai", NativeResponses: declaredNative},
		},
		Capabilities: caps,
		Routes: map[string][]config.Candidate{
			"alias-a": {{Provider: "anth", Model: "claude-up", Priority: 0}, {Provider: "oai", Model: "up-a", Priority: 1}},
		},
		Timeouts: Timeouts{Dial: time.Second, FirstToken: 10 * time.Second, ChunkIdle: 10 * time.Second},
		Pools: map[string]*keypool.Pool{
			"anth": keypool.NewPool([]keypool.KeyInput{{ID: "KA", Value: "k", Weight: 1}}),
			"oai":  keypool.NewPool([]keypool.KeyInput{{ID: "KB", Value: "k", Weight: 1}}),
		},
		RandIntn: func(n int) int { return 0 },
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	return &nativeTopo{messagesUp: msgsUp, openaiUp: openaiUp, p: p}
}

// TestNativeE2E_Messages直通非流式保真 验证多模态与扩展字段经直通零丢失，响应逐字节透传。
func TestNativeE2E_Messages直通非流式保真(t *testing.T) {
	up := testkit.StartJSONUpstream(t, 200, anthropicJSONBody)
	dead := httptest.NewServer(http404Handler{}) // oai 候选不参与，直通成功即返回
	t.Cleanup(dead.Close)
	oai := testkit.StartJSONUpstream(t, 200, usageJSONBody)
	tp := newNativeProxy(t, up, oai, false, nil)
	ts := httptest.NewServer(tp.p.HandlerMessages())
	t.Cleanup(ts.Close)

	raw := `{"model":"alias-a","max_tokens":64,"top_k":3,"metadata":{"user_id":"u"},"stop_sequences":["END"],` +
		`"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGk="}},` +
		`{"type":"text","text":"看图","cache_control":{"type":"ephemeral"}}]}]}`
	resp, body := postChat(t, ts.URL, raw)
	if resp.StatusCode != 200 {
		t.Fatalf("直通应 200: %d %s", resp.StatusCode, body)
	}
	// wire 保真：上游收到的除 model 外逐字节等于客户端原始体，image/cache_control/top_k 零丢失。
	want := strings.Replace(raw, `"model":"alias-a"`, `"model":"claude-up"`, 1)
	if string(up.LastBody()) != want {
		t.Fatalf("wire 保真破坏:\n got=%s\nwant=%s", up.LastBody(), want)
	}
	if got := up.LastHeader("x-api-key"); got != "k" {
		t.Fatalf("鉴权头不符: %q", got)
	}
	if up.LastHeader("anthropic-version") == "" {
		t.Fatal("anthropic-version 缺失")
	}
	// 响应逐字节透传，零反向映射，非 message 对象即直通证据。
	if body != anthropicJSONBody {
		t.Fatalf("响应应逐字节透传:\n got=%s\nwant=%s", body, anthropicJSONBody)
	}
	if n := oai.RequestCount(); n != 0 {
		t.Fatalf("直通成功不得触达第二候选: %d", n)
	}
	_ = dead
}

// TestNativeE2E_Messages直通流式真实usage 验证事件序列逐字节透传，message_start.usage.input_tokens
// 为上游真实值，翻译路径登记偏差恒 0 不出现，无合成首帧、无 [DONE] 透传。
func TestNativeE2E_Messages直通流式真实usage(t *testing.T) {
	up := testkit.StartStreamUpstream(t, anthropicStreamEvents())
	tp := newNativeProxy(t, up, testkit.StartJSONUpstream(t, 200, usageJSONBody), false, nil)
	ts := httptest.NewServer(tp.p.HandlerMessages())
	t.Cleanup(ts.Close)

	resp, body := postChat(t, ts.URL, `{"model":"alias-a","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if resp.StatusCode != 200 {
		t.Fatalf("直通应 200: %d %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, `"input_tokens":21`) {
		t.Fatalf("message_start 真实 usage 应直达: %s", body)
	}
	if strings.Count(body, "message_start") != 1 {
		t.Fatalf("直通不得合成首帧: %s", body)
	}
	if strings.Contains(body, "data: [DONE]") {
		t.Fatalf("直通不得合成/透传 [DONE]: %s", body)
	}
	// wire 保真：请求体除 model 外逐字节。
	want := `{"model":"claude-up","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"hi"}]}`
	if string(up.LastBody()) != want {
		t.Fatalf("wire 保真破坏: %s", up.LastBody())
	}
}

// TestNativeE2E_直通失败换道翻译writer切换 验证首字节前失败换道，客户端获单一完整翻译响应。
func TestNativeE2E_直通失败换道翻译writer切换(t *testing.T) {
	dead := testkit.StartStreamUpstream(t, []testkit.Event{{Data: "", CloseAfter: true}}) // 直通候选：立即断流，未过首 token，可换道
	up := testkit.StartStreamUpstream(t, testkit.NormalEvents())
	tp := newNativeProxy(t, dead, up, false, nil)
	ts := httptest.NewServer(tp.p.HandlerMessages())
	t.Cleanup(ts.Close)

	resp, body := postChat(t, ts.URL, `{"model":"alias-a","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if resp.StatusCode != 200 {
		t.Fatalf("换道后应 200: %d %s", resp.StatusCode, body)
	}
	// 第二候选走翻译路径：客户端收到翻译后的 Anthropic 事件，单一完整响应，无形态混用。
	if !strings.Contains(body, `"type":"message_start"`) || !strings.Contains(body, `"type":"message_stop"`) {
		t.Fatalf("换道翻译响应形态不符: %s", body)
	}
	if strings.Count(body, "message_start") != 1 || strings.Contains(body, `"input_tokens":21`) {
		t.Fatalf("不得混入直通形态: %s", body)
	}
}

// TestNativeE2E_失格换道升格400 验证直通候选失败回落翻译候选时出站前升格既有 400，不触达翻译上游。
func TestNativeE2E_失格换道升格400(t *testing.T) {
	dead := httptest.NewServer(http404Handler{})
	t.Cleanup(dead.Close)
	oai := testkit.StartJSONUpstream(t, 200, usageJSONBody)
	deadAnth := testkit.StartStreamUpstream(t, []testkit.Event{{Data: "", CloseAfter: true}})
	tp := newNativeProxy(t, deadAnth, oai, false, nil)
	ts := httptest.NewServer(tp.p.HandlerMessages())
	t.Cleanup(ts.Close)

	resp, body := postChat(t, ts.URL,
		`{"model":"alias-a","max_tokens":10,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64"}}]}]}`)
	if resp.StatusCode != 400 {
		t.Fatalf("换道落翻译候选应升格 400: %d %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, `"type":"error"`) || !strings.Contains(body, "不支持 image") {
		t.Fatalf("升格信封形态不符: %s", body)
	}
	if n := oai.RequestCount(); n != 0 {
		t.Fatalf("升格不得触达翻译上游: %d", n)
	}
	_ = dead
}

// TestNativeE2E_Responses声明直通 验证声明候选时 input_image 原生透传直通，未声明翻译兜底维持拒绝。
func TestNativeE2E_Responses声明直通(t *testing.T) {
	raw := `{"model":"alias-r","stream":false,"input":[{"type":"message","role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,xx"}]}]}`

	t.Run("声明直通", func(t *testing.T) {
		up := testkit.StartJSONUpstream(t, 200, `{"id":"resp_1","object":"response","status":"completed","output":[],"usage":{"input_tokens":13,"output_tokens":1}}`)
		reg := adapter.NewRegistry()
		reg.Register("oai", adapter.NewOpenAI(up.URL(), ""))
		p := New(Config{
			Registry:  reg,
			Providers: []config.Provider{{Name: "oai", Adapter: "openai", NativeResponses: true}},
			Routes:    map[string][]config.Candidate{"alias-r": {{Provider: "oai", Model: "up-r", Priority: 0}}},
			Timeouts:  Timeouts{Dial: time.Second, FirstToken: 10 * time.Second, ChunkIdle: 10 * time.Second},
			Pools:     map[string]*keypool.Pool{"oai": keypool.NewPool([]keypool.KeyInput{{ID: "KB", Value: "k", Weight: 1}})},
			Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		})
		ts := httptest.NewServer(p.HandlerResponses())
		t.Cleanup(ts.Close)

		resp, body := postChat(t, ts.URL, raw)
		if resp.StatusCode != 200 {
			t.Fatalf("声明候选应直通: %d %s", resp.StatusCode, body)
		}
		want := strings.Replace(raw, `"model":"alias-r"`, `"model":"up-r"`, 1)
		if string(up.LastBody()) != want {
			t.Fatalf("responses 直通 wire 保真破坏:\n got=%s\nwant=%s", up.LastBody(), want)
		}
		if body != `{"id":"resp_1","object":"response","status":"completed","output":[],"usage":{"input_tokens":13,"output_tokens":1}}` {
			t.Fatalf("响应应逐字节透传: %s", body)
		}
	})

	t.Run("未声明翻译兜底", func(t *testing.T) {
		oai := testkit.StartJSONUpstream(t, 200, usageJSONBody)
		reg := adapter.NewRegistry()
		reg.Register("oai", adapter.NewOpenAI(oai.URL(), ""))
		p := New(Config{
			Registry:  reg,
			Providers: []config.Provider{{Name: "oai", Adapter: "openai"}},
			Routes:    map[string][]config.Candidate{"alias-r": {{Provider: "oai", Model: "up-a", Priority: 0}}},
			Pools:     map[string]*keypool.Pool{"oai": keypool.NewPool([]keypool.KeyInput{{ID: "KB", Value: "k", Weight: 1}})},
			Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		})
		ts := httptest.NewServer(p.HandlerResponses())
		t.Cleanup(ts.Close)

		resp, body := postChat(t, ts.URL, raw)
		if resp.StatusCode != 400 || !strings.Contains(body, "input_item_unsupported") {
			t.Fatalf("未声明候选应维持翻译兜底拒绝: %d %s", resp.StatusCode, body)
		}
		if n := oai.RequestCount(); n != 0 {
			t.Fatalf("拒绝不得触达上游: %d", n)
		}
	})
}

// TestBridgeGate_Messages直通恢复工具多轮 验证 messages 入站加 anthropic 候选的工具多轮
// 直通正常服务，不豁免不拒绝；chat 入站同流量维持既有豁免边界。
func TestBridgeGate_Messages直通恢复工具多轮(t *testing.T) {
	up := testkit.StartJSONUpstream(t, 200, anthropicJSONBody)
	tp := newNativeProxy(t, up, testkit.StartJSONUpstream(t, 200, usageJSONBody), false, nil)
	ts := httptest.NewServer(tp.p.HandlerMessages())
	t.Cleanup(ts.Close)

	// 工具多轮上下文：tool_result 块，canonical 侧即 role=tool 标记。
	raw := `{"model":"alias-a","max_tokens":64,"messages":[` +
		`{"role":"user","content":[{"type":"text","text":"查天气"}]},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"c1","name":"weather","input":{"city":"北京"}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"c1","content":"晴"}]}]}`
	resp, body := postChat(t, ts.URL, raw)
	if resp.StatusCode != 200 {
		t.Fatalf("直通应正常服务工具多轮: %d %s", resp.StatusCode, body)
	}
	want := strings.Replace(raw, `"model":"alias-a"`, `"model":"claude-up"`, 1)
	if string(up.LastBody()) != want {
		t.Fatalf("工具多轮直通 wire 保真破坏: %s", up.LastBody())
	}
}

// TestNativeE2E_治理回落翻译 验证能力目录声明约束且实际干预时该 attempt 回落翻译路径，
// 干预触达出站 wire，留痕头在。
func TestNativeE2E_治理回落翻译(t *testing.T) {
	up := testkit.StartJSONUpstream(t, 200, anthropicJSONBody)
	tp := newNativeProxy(t, up, testkit.StartJSONUpstream(t, 200, usageJSONBody), false,
		[]config.ModelCapability{{Provider: "anth", Model: "claude-up", DropParams: []string{"top_k"}}})
	ts := httptest.NewServer(tp.p.HandlerMessages())
	t.Cleanup(ts.Close)

	resp, body := postChat(t, ts.URL, `{"model":"alias-a","max_tokens":64,"top_k":3,"messages":[{"role":"user","content":"hi"}]}`)
	if resp.StatusCode != 200 {
		t.Fatalf("治理回落应正常返回: %d %s", resp.StatusCode, body)
	}
	var probe map[string]any
	if err := json.Unmarshal([]byte(body), &probe); err != nil || probe["type"] != "message" {
		t.Fatalf("回落翻译路径应输出 message 对象(反向映射): %s", body)
	}
	if got := up.LastHeader("x-api-key"); got != "k" {
		t.Fatalf("回落翻译出站形态不符: %q", got)
	}
	// 翻译路径出站为 canonical 经桥接翻译重建的形态，非原字节。
	var out map[string]any
	if err := json.Unmarshal(up.LastBody(), &out); err != nil {
		t.Fatal(err)
	}
	if _, has := out["top_k"]; has {
		t.Fatalf("治理干预应触达出站 wire(top_k 被丢弃): %s", up.LastBody())
	}
}

type http404Handler struct{}

func (http404Handler) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(`{"error":"not found"}`))
}

// TestNativeE2E_失格治理升格400 验证失格请求仍受模型能力约束，且无冲突时仍可直通。
func TestNativeE2E_失格治理升格400(t *testing.T) {
	up := testkit.StartJSONUpstream(t, 200, anthropicJSONBody)
	tp := newNativeProxy(t, up, testkit.StartJSONUpstream(t, 200, usageJSONBody), false,
		[]config.ModelCapability{{Provider: "anth", Model: "claude-up", DropParams: []string{"top_k"}}})
	ts := httptest.NewServer(tp.p.HandlerMessages())
	t.Cleanup(ts.Close)

	resp, body := postChat(t, ts.URL,
		`{"model":"alias-a","max_tokens":10,"top_k":3,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGk="}}]}]}`)
	if resp.StatusCode != 400 {
		t.Fatalf("失格+声明约束应升格 400: %d %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, `"type":"error"`) || !strings.Contains(body, "不支持 image") {
		t.Fatalf("升格信封形态不符: %s", body)
	}
	if n := up.RequestCount(); n != 0 {
		t.Fatalf("升格不得触达上游: %d", n)
	}

	resp2, body2 := postChat(t, ts.URL,
		`{"model":"alias-a","max_tokens":10,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGk="}}]}]}`)
	if resp2.StatusCode != 200 || body2 != anthropicJSONBody {
		t.Fatalf("无冲突失格请求应直通 200: %d %s", resp2.StatusCode, body2)
	}
}
