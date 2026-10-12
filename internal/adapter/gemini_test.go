// Gemini 出站适配器契约测试(gemini-native-protocol;测试先行域):
// Prepare 逐字段 fixture(URL/头/体)、tool_choice 完整映射含 NONE、多模态 inlineData、
// 工具多轮完整翻译、Classify 判定表、NormalizeUsage、StreamEvent 事件机、RewriteResponse、
// 回环对拍(出站翻译 ∘ 入站翻译在可翻译子集域恒等)。
package adapter_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/solvela/muniu/internal/adapter"
	"github.com/solvela/muniu/internal/failover"
	"github.com/solvela/muniu/internal/protocol"
)

func geminiAdapter(t *testing.T) *adapter.Gemini {
	t.Helper()
	return adapter.NewGemini("https://gem.invalid/v1beta")
}

func chatFrom(t *testing.T, body string) *protocol.ChatRequest {
	t.Helper()
	req, err := protocol.ParseChatRequest([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	return req
}

// TestGemini_Prepare_逐字段 system 提升/tools 形态/tool_choice 含 NONE/generationConfig。
func TestGemini_Prepare_逐字段(t *testing.T) {
	g := geminiAdapter(t)

	t.Run("URL与鉴权头", func(t *testing.T) {
		out, err := g.Prepare(chatFrom(t, `{"model":"alias-a","messages":[{"role":"user","content":"hi"}]}`), "gemini-up", "gk-1")
		if err != nil {
			t.Fatal(err)
		}
		if out.URL != "https://gem.invalid/v1beta/models/gemini-up:generateContent" {
			t.Fatalf("URL 不符: %s", out.URL)
		}
		if got := out.Header.Get("x-goog-api-key"); got != "gk-1" {
			t.Fatalf("鉴权头不符: %q", got)
		}
		if !strings.Contains(string(out.Body), `"contents"`) {
			t.Fatalf("body 缺 contents: %s", out.Body)
		}
	})

	t.Run("流式URL", func(t *testing.T) {
		out, err := g.Prepare(chatFrom(t, `{"model":"a","stream":true,"messages":[{"role":"user","content":"hi"}]}`), "m", "k")
		if err != nil {
			t.Fatal(err)
		}
		if out.URL != "https://gem.invalid/v1beta/models/m:streamGenerateContent?alt=sse" {
			t.Fatalf("流式 URL 不符: %s", out.URL)
		}
	})

	t.Run("system提升与参数", func(t *testing.T) {
		out, err := g.Prepare(chatFrom(t,
			`{"model":"a","max_tokens":256,"temperature":0.5,"top_p":0.9,"top_k":40,"messages":[{"role":"system","content":"be nice"},{"role":"user","content":"hi"}]}`), "m", "k")
		if err != nil {
			t.Fatal(err)
		}
		var wire struct {
			SystemInstruction *struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"systemInstruction"`
			GenerationConfig struct {
				MaxOutputTokens int64   `json:"maxOutputTokens"`
				Temperature     float64 `json:"temperature"`
				TopP            float64 `json:"topP"`
				TopK            float64 `json:"topK"`
			} `json:"generationConfig"`
		}
		if err := json.Unmarshal(out.Body, &wire); err != nil {
			t.Fatal(err)
		}
		if wire.SystemInstruction == nil || wire.SystemInstruction.Parts[0].Text != "be nice" {
			t.Fatalf("system 提升不符: %s", out.Body)
		}
		if wire.GenerationConfig.MaxOutputTokens != 256 || wire.GenerationConfig.Temperature != 0.5 ||
			wire.GenerationConfig.TopP != 0.9 || wire.GenerationConfig.TopK != 40 {
			t.Fatalf("generationConfig 不符: %s", out.Body)
		}
	})

	t.Run("tool_choice完整映射含NONE", func(t *testing.T) {
		for choice, want := range map[string]string{
			`"auto"`:     `"mode":"AUTO"`,
			`"required"`: `"mode":"ANY"`,
			`"none"`:     `"mode":"NONE"`,
			`{"type":"function","function":{"name":"weather"}}`: `"allowedFunctionNames":["weather"]`,
		} {
			out, err := g.Prepare(chatFrom(t,
				`{"model":"a","tool_choice":`+choice+`,"tools":[{"type":"function","function":{"name":"weather","parameters":{"type":"object"}}}],"messages":[{"role":"user","content":"hi"}]}`), "m", "k")
			if err != nil {
				t.Fatalf("%s: %v", choice, err)
			}
			if !strings.Contains(string(out.Body), want) {
				t.Fatalf("tool_choice=%s 应产出 %s: %s", choice, want, out.Body)
			}
		}
	})

	t.Run("工具多轮完整翻译", func(t *testing.T) {
		out, err := g.Prepare(chatFrom(t, `{"model":"a","messages":[
			{"role":"user","content":"查天气"},
			{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"weather","arguments":"{\"city\":\"北京\"}"}}]},
			{"role":"tool","tool_call_id":"c1","content":"{\"temp\":\"晴\"}"},
			{"role":"user","content":"谢谢"}]}`), "m", "k")
		if err != nil {
			t.Fatalf("工具多轮应翻译完整: %v", err)
		}
		var wire struct {
			Contents []struct {
				Role  string `json:"role"`
				Parts []struct {
					Text         string `json:"text"`
					FunctionCall *struct {
						Name string          `json:"name"`
						Args json.RawMessage `json:"args"`
					} `json:"functionCall"`
					FunctionResponse *struct {
						Name     string          `json:"name"`
						Response json.RawMessage `json:"response"`
					} `json:"functionResponse"`
				} `json:"parts"`
			} `json:"contents"`
		}
		if err := json.Unmarshal(out.Body, &wire); err != nil {
			t.Fatal(err)
		}
		if len(wire.Contents) != 4 {
			t.Fatalf("contents 数不符: %s", out.Body)
		}
		if wire.Contents[1].Parts[0].FunctionCall == nil || wire.Contents[1].Parts[0].FunctionCall.Name != "weather" {
			t.Fatalf("functionCall 缺失: %s", out.Body)
		}
		if wire.Contents[2].Role != "user" || wire.Contents[2].Parts[0].FunctionResponse == nil ||
			wire.Contents[2].Parts[0].FunctionResponse.Name != "weather" {
			t.Fatalf("functionResponse 应归 role=user: %s", out.Body)
		}
	})

	t.Run("role_tool无配对拒绝", func(t *testing.T) {
		if _, err := g.Prepare(chatFrom(t, `{"model":"a","messages":[{"role":"tool","tool_call_id":"nope","content":"{}"}]}`), "m", "k"); err == nil {
			t.Fatal("无配对 tool 消息应拒绝")
		}
	})
}

// TestGemini_Prepare_多模态inlineData data URL → inlineData(mime/base64 与源一致)。
func TestGemini_Prepare_多模态inlineData(t *testing.T) {
	out, err := geminiAdapter(t).Prepare(chatFrom(t,
		`{"model":"a","messages":[{"role":"user","content":[{"type":"text","text":"看"},{"type":"image_url","image_url":{"url":"data:image/png;base64,aGk="}}]}]}`), "m", "k")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out.Body), `"inlineData":{"data":"aGk=","mimeType":"image/png"}`) {
		t.Fatalf("inlineData 形态不符: %s", out.Body)
	}
}

// TestGemini_Classify 判定表逐格。
func TestGemini_Classify(t *testing.T) {
	g := geminiAdapter(t)
	cases := []struct {
		status int
		body   string
		want   failover.ErrorClass
	}{
		{400, `{"error":{"code":400,"status":"INVALID_ARGUMENT"}}`, failover.ClassBadRequest},
		{429, `{}`, failover.ClassRateLimited},
		{200, `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED"}}`, failover.ClassRateLimited},
		{503, `{"error":{"status":"UNAVAILABLE"}}`, failover.ClassUpstream5xx},
		{200, `{"error":{"status":"DEADLINE_EXCEEDED"}}`, failover.ClassUpstream5xx},
		{401, `{}`, failover.ClassAuthFailed},
		{418, `{}`, failover.ClassUnknown},
	}
	for _, c := range cases {
		if got := g.Classify(c.status, []byte(c.body)); got != c.want {
			t.Fatalf("Classify(%d,%s)=%s want=%s", c.status, c.body, got, c.want)
		}
	}
}

// TestGemini_NormalizeUsage 归一表(含 thoughts→reasoning 口径)。
func TestGemini_NormalizeUsage(t *testing.T) {
	u, err := geminiAdapter(t).NormalizeUsage(json.RawMessage(
		`{"promptTokenCount":13,"candidatesTokenCount":5,"cachedContentTokenCount":3,"thoughtsTokenCount":2,"totalTokenCount":23}`))
	if err != nil || u.PromptTokens != 13 || u.CompletionTokens != 5 || u.CachedTokens != 3 || u.ReasoningTokens != 2 {
		t.Fatalf("归一不符: %+v %v", u, err)
	}
}

// TestGemini_StreamEvent_事件机 fixture 域:增量/工具/终帧/错误帧。
func TestGemini_StreamEvent_事件机(t *testing.T) {
	g := geminiAdapter(t)
	delta, meta, err := g.StreamEvent([]byte(`{"candidates":[{"content":{"parts":[{"text":"你好"}],"role":"model"},"index":0}]}`))
	if err != nil || !meta.HasContent || !strings.Contains(string(delta), `"content":"你好"`) {
		t.Fatalf("增量帧不符: %s %+v %v", delta, meta, err)
	}
	call, meta2, err := g.StreamEvent([]byte(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"weather","args":{"city":"北京"}}}],"role":"model"},"index":0}]}`))
	if err != nil || !meta2.HasContent || !strings.Contains(string(call), `"name":"weather"`) {
		t.Fatalf("工具帧不符: %s %+v %v", call, meta2, err)
	}
	final, meta3, err := g.StreamEvent([]byte(`{"candidates":[{"content":{"parts":[],"role":"model"},"index":0,"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":13,"candidatesTokenCount":5,"totalTokenCount":18}}`))
	if err != nil || !meta3.IsUsage || !strings.Contains(string(final), `"finish_reason":"stop"`) {
		t.Fatalf("终帧不符: %s %+v %v", final, meta3, err)
	}
	// 流内错误帧(HTTP 200 表象下的失真样式,百炼实证):报错交 proxy 归类
	if _, _, err := g.StreamEvent([]byte(`{"error":{"code":429,"message":"x","status":"RESOURCE_EXHAUSTED"}}`)); err == nil {
		t.Fatal("流内错误帧应报错")
	}
}

// TestGemini_StreamEvent_工具名转义 回归钉:名字含引号/反斜杠时 tool_calls id 不得裸拼接
// (裸拼接产出非法 JSON 帧,客户端解析失败且计量静默归零)。
func TestGemini_StreamEvent_工具名转义(t *testing.T) {
	g := geminiAdapter(t)
	raw, meta, err := g.StreamEvent([]byte(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"we\"ath\\er","args":{}}}],"role":"model"},"index":0}]}`))
	if err != nil || !meta.HasContent {
		t.Fatalf("工具帧应产出: %s %+v %v", raw, meta, err)
	}
	var chunk struct {
		Choices []struct {
			Delta struct {
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name string `json:"name"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &chunk); err != nil {
		t.Fatalf("含特殊字符的名字必须产出合法 JSON 帧: %v (%s)", err, raw)
	}
	tc := chunk.Choices[0].Delta.ToolCalls[0]
	if tc.ID != `call_we"ath\er` || tc.Function.Name != `we"ath\er` {
		t.Fatalf("id/name 应保真转义: %+v", tc)
	}
}

// TestGemini_RewriteResponse 非流式归一 + 安全拦截诚实呈现。
func TestGemini_RewriteResponse(t *testing.T) {
	g := geminiAdapter(t)
	out, usageRaw, err := g.RewriteResponse([]byte(
		`{"candidates":[{"content":{"parts":[{"text":"今天晴天"}],"role":"model"},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":13,"candidatesTokenCount":5,"cachedContentTokenCount":3},"modelVersion":"gemini-x"}`))
	if err != nil {
		t.Fatal(err)
	}
	var chat struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int64 `json:"prompt_tokens"`
			CompletionTokens int64 `json:"completion_tokens"`
			CachedTokens     int64 `json:"cached_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(out, &chat); err != nil {
		t.Fatal(err)
	}
	if chat.Choices[0].Message.Content != "今天晴天" || chat.Choices[0].FinishReason != "stop" ||
		chat.Usage.PromptTokens != 13 || chat.Usage.CompletionTokens != 5 || chat.Usage.CachedTokens != 3 {
		t.Fatalf("归一不符: %s %s", out, usageRaw)
	}
	// promptFeedback.blockReason → 报错(禁止空内容伪造成功)
	if _, _, err := g.RewriteResponse([]byte(`{"promptFeedback":{"blockReason":"SAFETY"}}`)); err == nil {
		t.Fatal("安全拦截应报错")
	}
}

// TestGemini_契约断言 桥接标记/翻译完整声明/流关闭即收尾。
func TestGemini_契约断言(t *testing.T) {
	g := geminiAdapter(t)
	if _, ok := adapter.Adapter(g).(adapter.Bridger); !ok {
		t.Fatal("gemini 应如实声明 Bridger")
	}
	if !adapter.Adapter(g).(adapter.ToolRoundtripCompleter).ToolRoundtripComplete() {
		t.Fatal("gemini 应声明工具多轮翻译完整")
	}
	if !adapter.Adapter(g).(adapter.StreamCloseDone).StreamCloseIsDone() {
		t.Fatal("gemini 应声明流关闭即收尾")
	}
	if _, ok := adapter.Adapter(g).(adapter.EmbeddingPreparer); ok {
		t.Fatal("gemini v1 不实现 embeddings")
	}
}

// TestGemini_StreamSequencer_跨帧并行工具与同帧聚合 回归钉组:
// ①每流测序跨帧 functionCall index 连续、同名 id 去重(Gemini wire 无工具序号);
// ②同帧双调用聚合为单 delta 的 tool_calls 数组;
// ③内容与 finishReason 同帧时产出单一合法 JSON(裸拼接曾产出非法 SSE data 行);
// ④非流式 RewriteResponse 同名 id 去重。
func TestGemini_StreamSequencer_跨帧并行工具与同帧聚合(t *testing.T) {
	g := geminiAdapter(t)
	seq := adapter.Adapter(g).(adapter.StreamSequencer).NewStreamSequencer()
	type tcDelta struct {
		Index    int    `json:"index"`
		ID       string `json:"id"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	parse := func(t *testing.T, raw []byte, err error) []tcDelta {
		t.Helper()
		if err != nil {
			t.Fatalf("测序帧报错: %v", err)
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					ToolCalls []tcDelta `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(raw, &chunk); err != nil {
			t.Fatalf("须为合法 JSON 帧: %v (%s)", err, raw)
		}
		return chunk.Choices[0].Delta.ToolCalls
	}

	one := func(data string) []tcDelta {
		raw, _, err := seq([]byte(data))
		return parse(t, raw, err)
	}
	c1 := one(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"weather","args":{}}}],"role":"model"},"index":0}]}`)
	c2 := one(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"calendar","args":{}}}],"role":"model"},"index":0}]}`)
	c3 := one(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"weather","args":{}}}],"role":"model"},"index":0}]}`)
	if c1[0].Index != 0 || c2[0].Index != 1 || c3[0].Index != 2 {
		t.Fatalf("跨帧 index 应连续编号: %d %d %d", c1[0].Index, c2[0].Index, c3[0].Index)
	}
	if c3[0].ID != "call_weather_2" {
		t.Fatalf("同名调用 id 应按序去重: %s", c3[0].ID)
	}

	// 同帧双调用:单 delta 携带 tool_calls 数组(禁止多 JSON 对象裸拼接进同一 data 行)
	raw, _, err := g.StreamEvent([]byte(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"a","args":{}}},{"functionCall":{"name":"b","args":{}}}],"role":"model"},"index":0}]}`))
	calls := parse(t, raw, err)
	if len(calls) != 2 || calls[0].Index != 0 || calls[1].Index != 1 || calls[1].Function.Name != "b" {
		t.Fatalf("同帧双调用应为单 delta 数组: %s", raw)
	}

	// 内容与 finishReason 同帧:单一合法 JSON 且 meta 双置位
	raw, meta, err := g.StreamEvent([]byte(`{"candidates":[{"content":{"parts":[{"text":"收尾"}],"role":"model"},"index":0,"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":1,"totalTokenCount":4}}`))
	if err != nil || !meta.HasContent || !meta.IsUsage {
		t.Fatalf("同帧聚合 meta 不符: %+v %v", meta, err)
	}
	var chunk struct {
		Choices []struct {
			Delta struct {
				Content string `json:"content"`
			} `json:"delta"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage json.RawMessage `json:"usage"`
	}
	if err := json.Unmarshal(raw, &chunk); err != nil {
		t.Fatalf("同帧内容+finish 必须为单一合法 JSON: %v (%s)", err, raw)
	}
	if chunk.Choices[0].Delta.Content != "收尾" || chunk.Choices[0].FinishReason != "stop" || len(chunk.Usage) == 0 {
		t.Fatalf("同帧聚合内容不符: %s", raw)
	}

	// 非流式同名 id 去重
	out, _, err := g.RewriteResponse([]byte(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"w","args":{}}},{"functionCall":{"name":"w","args":{}}}],"role":"model"},"finishReason":"STOP","index":0}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"id":"call_w_2"`) {
		t.Fatalf("非流式同名 id 应去重: %s", out)
	}
}
