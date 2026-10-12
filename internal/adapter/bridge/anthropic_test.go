// Anthropic 桥接单测:请求翻译(system 提升/max_tokens 缺省/tools 重排/x-api-key 头)、
// 非流式响应翻译(content/tool_use/stop_reason/usage)、流式事件机关键帧。
package bridge_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/solvela/muniu/internal/adapter"
	"github.com/solvela/muniu/internal/protocol"
)

func newBridge(t *testing.T) adapter.Adapter {
	t.Helper()
	ad, err := adapter.For("anthropic", "https://api.anthropic.com", "sk-ant-test")
	if err != nil {
		t.Fatalf("构造桥接: %v", err)
	}
	return ad
}

func TestBridge_Prepare_请求翻译(t *testing.T) {
	ad := newBridge(t)
	req, err := protocol.ParseChatRequest([]byte(`{
		"model":"alias","max_tokens":512,"temperature":0.2,
		"messages":[
			{"role":"system","content":"你是木牛"},
			{"role":"user","content":"你好"},
			{"role":"user","content":"再来一遍"}
		],
		"tools":[{"type":"function","function":{"name":"get_weather","description":"查天气","parameters":{"type":"object"}}}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	out, err := ad.Prepare(req, "claude-sonnet-4-5", "sk-ant-key")
	if err != nil {
		t.Fatal(err)
	}
	if out.URL != "https://api.anthropic.com/v1/messages" {
		t.Fatalf("URL 不符: %s", out.URL)
	}
	if out.Header.Get("x-api-key") != "sk-ant-key" || out.Header.Get("anthropic-version") != "2023-06-01" {
		t.Fatalf("桥接头不符: %v", out.Header)
	}
	var body struct {
		Model     string `json:"model"`
		MaxTokens int64  `json:"max_tokens"`
		System    string `json:"system"`
		Messages  []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(out.Body, &body); err != nil {
		t.Fatal(err)
	}
	if body.Model != "claude-sonnet-4-5" || body.MaxTokens != 512 {
		t.Fatalf("model/max_tokens 不符: %+v", body)
	}
	if body.System != "你是木牛" {
		t.Fatalf("system 未提升: %q", body.System)
	}
	// 连续 user 合并为一条
	if len(body.Messages) != 1 || !strings.Contains(body.Messages[0].Content, "再来一遍") {
		t.Fatalf("同角色合并不符: %+v", body.Messages)
	}
	if len(body.Tools) != 1 || body.Tools[0].Name != "get_weather" {
		t.Fatalf("tools 翻译不符: %+v", body.Tools)
	}
}

func TestBridge_Prepare_maxTokens缺省(t *testing.T) {
	ad := newBridge(t)
	req, err := protocol.ParseChatRequest([]byte(`{"model":"m","messages":[{"role":"user","content":"x"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	out, err := ad.Prepare(req, "claude-sonnet-4-5", "k")
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		MaxTokens int64 `json:"max_tokens"`
	}
	_ = json.Unmarshal(out.Body, &body)
	if body.MaxTokens != 4096 {
		t.Fatalf("max_tokens 缺省应 4096: %d", body.MaxTokens)
	}
}

func TestBridge_RewriteResponse_非流式翻译(t *testing.T) {
	ad := newBridge(t)
	anthropicBody := []byte(`{
		"content":[
			{"type":"thinking","thinking":"思考过程"},
			{"type":"text","text":"9.8 更大"},
			{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{"city":"北京"}}
		],
		"stop_reason":"tool_use",
		"usage":{"input_tokens":30,"output_tokens":12,"cache_read_input_tokens":8}
	}`)
	out, usage, err := ad.RewriteResponse(anthropicBody)
	if err != nil {
		t.Fatal(err)
	}
	var resp struct {
		Choices []struct {
			Message struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
				ToolCalls        []struct {
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatal(err)
	}
	c := resp.Choices[0]
	if c.Message.Content != "9.8 更大" || c.FinishReason != "tool_calls" {
		t.Fatalf("翻译不符: %+v", c)
	}
	if c.Message.ReasoningContent != "思考过程" {
		t.Fatalf("thinking 未翻译: %q", c.Message.ReasoningContent)
	}
	if len(c.Message.ToolCalls) != 1 || c.Message.ToolCalls[0].Function.Name != "get_weather" {
		t.Fatalf("tool_use 未翻译: %+v", c.Message.ToolCalls)
	}
	u, err := ad.NormalizeUsage(usage)
	if err != nil {
		t.Fatal(err)
	}
	if u.PromptTokens != 30 || u.CompletionTokens != 12 || u.CachedTokens != 8 {
		t.Fatalf("usage 不符: %+v", u)
	}
}

func TestBridge_StreamEvent_事件机(t *testing.T) {
	ad := newBridge(t)

	// message_start:usage 帧(prompt)
	passthrough, meta, err := ad.StreamEvent([]byte(`{"type":"message_start","message":{"usage":{"input_tokens":21,"cache_read_input_tokens":4}}}`))
	if err != nil || !meta.IsUsage || meta.HasContent {
		t.Fatalf("message_start meta 不符: %+v %v", meta, err)
	}
	u, _ := ad.NormalizeUsage(json.RawMessage(extractUsage(t, passthrough)))
	if u.PromptTokens != 21 || u.CachedTokens != 4 {
		t.Fatalf("message_start usage 不符: %+v", u)
	}

	// thinking_delta:实质内容(首 token 语义)
	_, meta, err = ad.StreamEvent([]byte(`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"嗯"}}`))
	if err != nil || !meta.HasContent {
		t.Fatalf("thinking_delta 不符: %+v %v", meta, err)
	}

	// text_delta:内容透出在 delta.content
	passthrough, meta, err = ad.StreamEvent([]byte(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"答案"}}`))
	if err != nil || !meta.HasContent || !strings.Contains(string(passthrough), `"content":"答案"`) {
		t.Fatalf("text_delta 不符: %q %+v %v", passthrough, meta, err)
	}

	// ping:对客户端不可见
	passthrough, meta, err = ad.StreamEvent([]byte(`{"type":"ping"}`))
	if err != nil || passthrough != nil || meta.HasContent {
		t.Fatalf("ping 应不可见: %q %+v %v", passthrough, meta, err)
	}

	// message_delta:finish_reason + completion tokens
	passthrough, meta, err = ad.StreamEvent([]byte(`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":7}}`))
	if err != nil || !meta.IsUsage || !strings.Contains(string(passthrough), `"finish_reason":"stop"`) {
		t.Fatalf("message_delta 不符: %q %+v %v", passthrough, meta, err)
	}

	// message_stop:[DONE]
	passthrough, meta, err = ad.StreamEvent([]byte(`{"type":"message_stop"}`))
	if err != nil || !meta.IsDone || string(passthrough) != "[DONE]" {
		t.Fatalf("message_stop 不符: %q %+v %v", passthrough, meta, err)
	}

	// 流内错误事件:报错供上层归类
	if _, _, err := ad.StreamEvent([]byte(`{"type":"error","error":{"message":"overloaded"}}`)); err == nil {
		t.Fatal("错误事件必须报错")
	}
}

func TestBridge_StreamEvent_toolUse翻译(t *testing.T) {
	ad := newBridge(t)

	// content_block_start(tool_use):首个 tool_call delta 必须携带 index/id/name
	// (OpenAI 流式契约;缺 id/name 客户端拿到一串参数片段却不知道调哪个函数)
	pt, meta, err := ad.StreamEvent([]byte(`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_9","name":"get_weather","input":{}}}`))
	if err != nil || !meta.HasContent {
		t.Fatalf("tool_use start meta 不符: %+v %v", meta, err)
	}
	var startChunk struct {
		Choices []struct {
			Delta struct {
				ToolCalls []struct {
					Index    int    `json:"index"`
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(pt, &startChunk); err != nil {
		t.Fatal(err)
	}
	tc := startChunk.Choices[0].Delta.ToolCalls
	if len(tc) != 1 || tc[0].Index != 1 || tc[0].ID != "toolu_9" || tc[0].Type != "function" || tc[0].Function.Name != "get_weather" {
		t.Fatalf("tool_use start 翻译不符: %s", pt)
	}

	// input_json_delta:arguments 增量挂在同块序号上(并行工具调用按序号分离,不串扰)
	pt2, _, err := ad.StreamEvent([]byte(`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"city\":\"北京\"}"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(pt2), `"index":1`) || !strings.Contains(string(pt2), `\"city\":\"北京\"`) {
		t.Fatalf("input_json_delta 翻译不符: %s", pt2)
	}

	// content_block_start(text):对客户端不可见
	pt3, meta3, err := ad.StreamEvent([]byte(`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`))
	if err != nil || pt3 != nil || meta3.HasContent {
		t.Fatalf("text 块起始应不可见: %q %+v %v", pt3, meta3, err)
	}
}

func TestBridge_StreamEvent_空载荷与未知stop(t *testing.T) {
	ad := newBridge(t)
	// 空 data/纯注释事件(SSE 心跳):不报错、不产出翻译帧,否则心跳会触发断流出口
	pt, meta, err := ad.StreamEvent(nil)
	if err != nil || pt != nil || meta.HasContent || meta.IsUsage {
		t.Fatalf("空载荷应不可见: %q %+v %v", pt, meta, err)
	}
	// model_context_window_exceeded → length(OpenAI 对应语义,不得伪装成正常 stop)
	pt, _, err = ad.StreamEvent([]byte(`{"type":"message_delta","delta":{"stop_reason":"model_context_window_exceeded"},"usage":{"output_tokens":1}}`))
	if err != nil || !strings.Contains(string(pt), `"finish_reason":"length"`) {
		t.Fatalf("context_window_exceeded 应映射 length: %q %v", pt, err)
	}
	// refusal → content_filter
	pt, _, err = ad.StreamEvent([]byte(`{"type":"message_delta","delta":{"stop_reason":"refusal"},"usage":{"output_tokens":1}}`))
	if err != nil || !strings.Contains(string(pt), `"finish_reason":"content_filter"`) {
		t.Fatalf("refusal 应映射 content_filter: %q %v", pt, err)
	}
}

func TestBridge_Prepare_tools非法拒绝(t *testing.T) {
	ad := newBridge(t)
	// tools 字段形态非法 → 显式报错;静默丢弃会让上游在无工具状态下作答且无任何痕迹
	req, err := protocol.ParseChatRequest([]byte(`{"model":"m","messages":[{"role":"user","content":"x"}],"tools":{"a":1}}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ad.Prepare(req, "claude-sonnet-4-5", "k"); err == nil {
		t.Fatal("tools 解析失败必须报错")
	}
}

func TestBridge_Prepare_参数透传与tool_choice(t *testing.T) {
	ad := newBridge(t)
	req, err := protocol.ParseChatRequest([]byte(`{
		"model":"alias","max_tokens":512,"top_p":0.9,
		"tool_choice":{"type":"function","function":{"name":"get_weather"}},
		"stop":["END"],"messages":[{"role":"user","content":"x"}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	out, err := ad.Prepare(req, "claude-sonnet-4-5", "k")
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		TopP       float64 `json:"top_p"`
		ToolChoice struct {
			Type string `json:"type"`
			Name string `json:"name"`
		} `json:"tool_choice"`
		Stop any `json:"stop"`
	}
	if err := json.Unmarshal(out.Body, &body); err != nil {
		t.Fatal(err)
	}
	if body.TopP != 0.9 {
		t.Fatalf("top_p 应透传: %v", body.TopP)
	}
	if body.ToolChoice.Type != "tool" || body.ToolChoice.Name != "get_weather" {
		t.Fatalf("tool_choice 应映射 tool+name: %+v", body.ToolChoice)
	}
	if body.Stop != nil {
		t.Fatalf("stop 为已登记限制,不得透传: %v", body.Stop)
	}
}

func TestBridge_RewriteResponse_标识透出(t *testing.T) {
	ad := newBridge(t)
	out, _, err := ad.RewriteResponse([]byte(`{
		"id":"msg_01ABC","model":"claude-sonnet-4-5",
		"content":[{"type":"text","text":"答案"}],
		"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":2}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	var resp struct {
		ID    string `json:"id"`
		Model string `json:"model"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.ID != "msg_01ABC" || resp.Model != "claude-sonnet-4-5" {
		t.Fatalf("id/model 应透出: %+v", resp)
	}
}

// extractUsage 从翻译后的 usage 帧抽出内层 usage JSON。
func extractUsage(t *testing.T, chunk []byte) string {
	t.Helper()
	var frame struct {
		Usage json.RawMessage `json:"usage"`
	}
	if err := json.Unmarshal(chunk, &frame); err != nil || len(frame.Usage) == 0 {
		t.Fatalf("usage 帧非法: %s", chunk)
	}
	return string(frame.Usage)
}

// TestBridge_Prepare_鉴权形态声明 声明制鉴权(config.Provider.Auth → adapter.Options):
// 缺省=x-api-key(协议标准,官方 Anthropic/SDK 口径);bearer=Authorization Bearer(国内分支口径),
// 且两形态互斥(声明 bearer 不得残留 x-api-key,反之亦然)。
func TestBridge_Prepare_鉴权形态声明(t *testing.T) {
	build := func(t *testing.T, auth string) adapter.Adapter {
		t.Helper()
		ad, err := adapter.For("anthropic", "https://api.example.com", "sk-test", adapter.Options{Auth: auth})
		if err != nil {
			t.Fatalf("构造桥接(auth=%q): %v", auth, err)
		}
		return ad
	}
	prepare := func(t *testing.T, ad adapter.Adapter) adapter.Outbound {
		t.Helper()
		out, err := ad.Prepare(&protocol.ChatRequest{
			Model: "m",
			Raw:   []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`),
		}, "m", "sk-test")
		if err != nil {
			t.Fatalf("Prepare: %v", err)
		}
		return *out
	}

	def := prepare(t, build(t, ""))
	if def.Header.Get("x-api-key") != "sk-test" || def.Header.Get("Authorization") != "" {
		t.Fatalf("缺省应 x-api-key 且无 Authorization: %v", def.Header)
	}
	bearer := prepare(t, build(t, adapter.AuthBearer))
	if bearer.Header.Get("Authorization") != "Bearer sk-test" || bearer.Header.Get("x-api-key") != "" {
		t.Fatalf("bearer 声明应仅发 Authorization: %v", bearer.Header)
	}
	if def.Header.Get("anthropic-version") != bearer.Header.Get("anthropic-version") {
		t.Fatalf("anthropic-version 两形态都必须存在: %v / %v", def.Header, bearer.Header)
	}
	// 非法值:config 已拦截,adapter 层 fail-fast 兜底(控制面下发路径绕过 config.Load)
	if _, err := adapter.For("anthropic", "https://x", "k", adapter.Options{Auth: "x-api-key"}); err == nil {
		t.Fatal("auth 仅允许 空|bearer,非法值应报错")
	}
}

// TestBridge_Prepare_工具多轮往返 请求侧翻译完整化(anthropic-bridge-tool-roundtrip):
// assistant.tool_calls→tool_use 块(input 对象)/role=tool→user tool_result 块/连续 tool
// 合并单 user/未配对 id 报错/纯文本 wire 逐字节不变(字符串 content 守门)。
func TestBridge_Prepare_工具多轮往返(t *testing.T) {
	ad := newBridge(t)

	t.Run("工具全轮块形态", func(t *testing.T) {
		req, err := protocol.ParseChatRequest([]byte(`{
			"model":"alias","max_tokens":512,
			"messages":[
				{"role":"user","content":"北京天气如何"},
				{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"北京\"}"}}]},
				{"role":"tool","tool_call_id":"call_1","content":"晴 25°C"}
			],
			"tools":[{"type":"function","function":{"name":"get_weather","parameters":{"type":"object"}}}]
		}`))
		if err != nil {
			t.Fatal(err)
		}
		out, err := ad.Prepare(req, "claude-sonnet-4-5", "k")
		if err != nil {
			t.Fatal(err)
		}
		var body struct {
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(out.Body, &body); err != nil {
			t.Fatal(err)
		}
		if len(body.Messages) != 3 {
			t.Fatalf("消息数不符: %+v", body.Messages)
		}
		var blocks []struct {
			Type      string          `json:"type"`
			Text      string          `json:"text,omitempty"`
			ID        string          `json:"id,omitempty"`
			Name      string          `json:"name,omitempty"`
			Input     json.RawMessage `json:"input,omitempty"`
			ToolUseID string          `json:"tool_use_id,omitempty"`
			Content   string          `json:"content,omitempty"`
		}
		// assistant:content null + tool_calls → 仅 tool_use 块(input 对象形态)
		if err := json.Unmarshal(body.Messages[1].Content, &blocks); err != nil {
			t.Fatalf("assistant 应为块数组: %s", body.Messages[1].Content)
		}
		if body.Messages[1].Role != "assistant" || len(blocks) != 1 ||
			blocks[0].Type != "tool_use" || blocks[0].ID != "call_1" || blocks[0].Name != "get_weather" ||
			string(blocks[0].Input) != `{"city":"北京"}` {
			t.Fatalf("tool_use 块不符: %+v", blocks)
		}
		// tool 结果:合并进单条 user 的 tool_result 块
		blocks = nil
		if err := json.Unmarshal(body.Messages[2].Content, &blocks); err != nil {
			t.Fatalf("tool 结果应为块数组: %s", body.Messages[2].Content)
		}
		if body.Messages[2].Role != "user" || len(blocks) != 1 ||
			blocks[0].Type != "tool_result" || blocks[0].ToolUseID != "call_1" || blocks[0].Content != "晴 25°C" {
			t.Fatalf("tool_result 块不符: %+v", blocks)
		}
		// 交替:user → assistant → user
		if body.Messages[0].Role != "user" {
			t.Fatalf("角色序列不符: %+v", body.Messages)
		}
	})

	t.Run("连续tool消息合并单user", func(t *testing.T) {
		req, err := protocol.ParseChatRequest([]byte(`{
			"model":"alias","max_tokens":64,
			"messages":[
				{"role":"user","content":"查两地"},
				{"role":"assistant","content":null,"tool_calls":[
					{"id":"call_a","type":"function","function":{"name":"w","arguments":"{}"}},
					{"id":"call_b","type":"function","function":{"name":"w","arguments":"{}"}}
				]},
				{"role":"tool","tool_call_id":"call_a","content":"A"},
				{"role":"tool","tool_call_id":"call_b","content":"B"},
				{"role":"user","content":"谢谢"}
			]
		}`))
		if err != nil {
			t.Fatal(err)
		}
		out, err := ad.Prepare(req, "m", "k")
		if err != nil {
			t.Fatal(err)
		}
		var body struct {
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		_ = json.Unmarshal(out.Body, &body)
		// user / assistant(tool_use×2) / user(tool_result×2+text 合并) 三条,交替无断裂
		if len(body.Messages) != 3 {
			t.Fatalf("应合并为 3 条消息: %+v", body.Messages)
		}
		if !strings.Contains(string(body.Messages[2].Content), `"tool_use_id":"call_a"`) ||
			!strings.Contains(string(body.Messages[2].Content), `"tool_use_id":"call_b"`) {
			t.Fatalf("两个 tool_result 应在同一 user 消息: %s", body.Messages[2].Content)
		}
	})

	t.Run("未配对tool_call_id拒绝", func(t *testing.T) {
		req, err := protocol.ParseChatRequest([]byte(`{
			"model":"alias","max_tokens":64,
			"messages":[
				{"role":"user","content":"hi"},
				{"role":"tool","tool_call_id":"call_ghost","content":"x"}
			]
		}`))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ad.Prepare(req, "m", "k"); err == nil {
			t.Fatal("未配对 tool_call_id 应报错")
		}
	})

	t.Run("纯文本wire逐字节不变", func(t *testing.T) {
		req, err := protocol.ParseChatRequest([]byte(`{
			"model":"alias","max_tokens":64,"temperature":0.3,
			"messages":[
				{"role":"system","content":"sys"},
				{"role":"user","content":"你好"},
				{"role":"user","content":"再来"}
			]
		}`))
		if err != nil {
			t.Fatal(err)
		}
		out, err := ad.Prepare(req, "m", "k")
		if err != nil {
			t.Fatal(err)
		}
		want := `{"max_tokens":64,"messages":[{"role":"user","content":"你好\n\n再来"}],"model":"m","system":"sys","temperature":0.3}`
		if string(out.Body) != want {
			t.Fatalf("纯文本 wire 应逐字节不变:\n got=%s\nwant=%s", out.Body, want)
		}
	})
}
