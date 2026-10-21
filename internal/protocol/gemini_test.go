// Gemini 协议层测试：入站翻译逐格、拒绝清单、非流式与流式反向映射，
// 以及回环对拍——出站翻译与入站翻译复合后在可翻译子集域为恒等。
package protocol

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

// geminiCorpus 语料读取自 conformance/gemini/native/corpus.json，多个方向共用。
func geminiCorpus(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile("../../conformance/gemini/native/corpus.json")
	if err != nil {
		t.Fatalf("读语料: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// TestTranslateGeminiRequest_合法映射 语料请求逐格翻译：systemInstruction/工具全链/
// inlineData 多模态/generationConfig 同义映射。
func TestTranslateGeminiRequest_合法映射(t *testing.T) {
	corpus := geminiCorpus(t)
	var req0 struct {
		PathModel string          `json:"path_model"`
		Body      json.RawMessage `json:"body"`
	}
	if err := json.Unmarshal(corpus["request"], &req0); err != nil {
		t.Fatal(err)
	}
	req, err := TranslateGeminiRequest(req0.PathModel, req0.Body, false)
	if err != nil {
		t.Fatalf("语料请求应翻译成功: %v", err)
	}
	if req.Model != "gemini-x" {
		t.Fatalf("别名应取自路径: %s", req.Model)
	}
	raw := string(req.Raw)
	// systemInstruction 提升
	if !strings.Contains(raw, `"role":"system","content":"be nice"`) {
		t.Fatalf("systemInstruction 应提升为 system: %s", raw)
	}
	// functionCall → tool_calls，合成 id
	if !strings.Contains(raw, `"tool_calls":[{"id":"call_weather","type":"function","function":{"name":"weather"`) {
		t.Fatalf("functionCall 应映射 tool_calls: %s", raw)
	}
	// functionResponse → role=tool，tool_call_id 回指
	if !strings.Contains(raw, `"role":"tool","tool_call_id":"call_weather"`) {
		t.Fatalf("functionResponse 应映射 tool 消息: %s", raw)
	}
	// inlineData → image_url data URL
	if !strings.Contains(raw, `data:image/png;base64,aGk=`) {
		t.Fatalf("inlineData 应映射 image_url: %s", raw)
	}
	// generationConfig 同义
	for _, want := range []string{`"temperature":0.5`, `"top_p":0.9`, `"top_k":40`, `"max_tokens":1024`, `"stop":["END"]`} {
		if !strings.Contains(raw, want) {
			t.Fatalf("generationConfig 缺 %s: %s", want, raw)
		}
	}
	// tools 形态移入 function
	if !strings.Contains(raw, `"type":"function","function":{"name":"weather"`) {
		t.Fatalf("functionDeclarations 应映射 tools: %s", raw)
	}
	// toolConfig AUTO → auto
	if !strings.Contains(raw, `"tool_choice":"auto"`) {
		t.Fatalf("toolConfig 应映射 tool_choice: %s", raw)
	}
}

// TestTranslateGeminiRequest_拒绝清单 显式拒绝与静默丢弃的负面用例。
func TestTranslateGeminiRequest_拒绝清单(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		code   string
		status string
	}{
		{"fileData拒绝", `{"contents":[{"role":"user","parts":[{"fileData":{"fileUri":"gs://x"}}]}]}`, CodeGeminiContentPartUnsupported, "INVALID_ARGUMENT"},
		{"音频inlineData拒绝", `{"contents":[{"role":"user","parts":[{"inlineData":{"mimeType":"audio/wav","data":"aGk="}}]}]}`, CodeGeminiContentPartUnsupported, "INVALID_ARGUMENT"},
		{"空contents", `{"contents":[]}`, CodeGeminiInputInvalid, "INVALID_ARGUMENT"},
		{"未知role", `{"contents":[{"role":"system","parts":[{"text":"x"}]}]}`, CodeGeminiInputInvalid, "INVALID_ARGUMENT"},
		{"response先于call", `{"contents":[{"role":"user","parts":[{"functionResponse":{"name":"f","response":{}}}]}]}`, CodeGeminiInputInvalid, "INVALID_ARGUMENT"},
		{"非法toolConfig", `{"contents":[{"role":"user","parts":[{"text":"x"}]}],"toolConfig":{"functionCallingConfig":{"mode":"BOGUS"}}}`, CodeGeminiInputInvalid, "INVALID_ARGUMENT"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := TranslateGeminiRequest("m", []byte(c.body), false)
			var ge *GeminiUnsupportedError
			if !errors.As(err, &ge) || ge.Code != c.code || ge.Status != c.status {
				t.Fatalf("拒绝不符: %v", err)
			}
		})
	}
	// 静默丢弃项正常服务：safetySettings/topK/candidateCount/seed
	req, err := TranslateGeminiRequest("m", []byte(
		`{"contents":[{"role":"user","parts":[{"text":"x"}]}],"safetySettings":[{"category":"HARM","threshold":"LOW"}],"generationConfig":{"topK":3,"candidateCount":1,"seed":7}}`), false)
	if err != nil {
		t.Fatalf("静默丢弃项应正常服务: %v", err)
	}
	if strings.Contains(string(req.Raw), "safetySettings") || strings.Contains(string(req.Raw), "topK") {
		t.Fatalf("丢弃字段不得进入 canonical: %s", req.Raw)
	}
	// tool_choice 完整映射，含 NONE
	for mode, want := range map[string]string{"NONE": `"tool_choice":"none"`, "ANY": `"tool_choice":"required"`, "AUTO": `"tool_choice":"auto"`} {
		req, err := TranslateGeminiRequest("m", []byte(
			`{"contents":[{"role":"user","parts":[{"text":"x"}]}],"toolConfig":{"functionCallingConfig":{"mode":"`+mode+`"}}}`), false)
		if err != nil || !strings.Contains(string(req.Raw), want) {
			t.Fatalf("mode=%s 映射不符: %v %s", mode, err, req.Raw)
		}
	}
}

// TestGenerateContentResponseFromChat_非流式反向映射 语料响应域逐格与 chat→gemini 固定映射。
func TestGenerateContentResponseFromChat_非流式反向映射(t *testing.T) {
	chat := []byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":"今天晴天"},"finish_reason":"stop"}],"usage":{"prompt_tokens":13,"completion_tokens":5,"cached_tokens":3,"reasoning_tokens":2}}`)
	out, err := GenerateContentResponseFromChat(chat, "gemini-x")
	if err != nil {
		t.Fatal(err)
	}
	var resp struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text    string `json:"text"`
					Thought bool   `json:"thought"`
				} `json:"parts"`
				Role string `json:"role"`
			} `json:"content"`
			FinishReason string `json:"finishReason"`
		} `json:"candidates"`
		UsageMetadata struct {
			PromptTokenCount        int64 `json:"promptTokenCount"`
			CandidatesTokenCount    int64 `json:"candidatesTokenCount"`
			CachedContentTokenCount int64 `json:"cachedContentTokenCount"`
			ThoughtsTokenCount      int64 `json:"thoughtsTokenCount"`
			TotalTokenCount         int64 `json:"totalTokenCount"`
		} `json:"usageMetadata"`
		ModelVersion string `json:"modelVersion"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("非 Gemini 形态: %v (%s)", err, out)
	}
	if len(resp.Candidates) != 1 || resp.Candidates[0].Content.Role != "model" ||
		resp.Candidates[0].FinishReason != "STOP" || len(resp.Candidates[0].Content.Parts) != 1 ||
		resp.Candidates[0].Content.Parts[0].Text != "今天晴天" {
		t.Fatalf("candidates 形态不符: %s", out)
	}
	u := resp.UsageMetadata
	if u.PromptTokenCount != 13 || u.CandidatesTokenCount != 5 || u.CachedContentTokenCount != 3 ||
		u.ThoughtsTokenCount != 2 || u.TotalTokenCount != 18 {
		t.Fatalf("usageMetadata 映射不符: %+v", u)
	}
	if resp.ModelVersion != "gemini-x" {
		t.Fatalf("modelVersion 应回显: %s", out)
	}
	// tool_calls → functionCall 的 args 为对象形态；finish_reason=tool_calls → STOP
	chatTool := []byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"weather","arguments":"{\"city\":\"北京\"}"}}]},"finish_reason":"tool_calls"}],"usage":null}`)
	out2, err := GenerateContentResponseFromChat(chatTool, "m")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out2), `"functionCall"`) || !strings.Contains(string(out2), `"name":"weather"`) ||
		!strings.Contains(string(out2), `"city":"北京"`) {
		t.Fatalf("functionCall 形态不符: %s", out2)
	}
}

// TestGeminiStreamEncoder_事件序列 语料流帧域：增量帧在前、终帧收尾、[DONE] 语义由 Complete 承担。
func TestGeminiStreamEncoder_事件序列(t *testing.T) {
	corpus := geminiCorpus(t)
	var frames []string
	if err := json.Unmarshal(corpus["stream_frames"], &frames); err != nil {
		t.Fatal(err)
	}
	// 语料流帧即上游 gemini 形态，此处反向：以出站适配器同款帧结构构造 chat 流，映射回语料域
	enc := NewGeminiStreamEncoder("gemini-x")
	var out [][]byte
	out = append(out, enc.Feed([]byte(`{"choices":[{"index":0,"delta":{"content":"今天"}}]}`))...)
	out = append(out, enc.Feed([]byte(`{"choices":[{"index":0,"delta":{"content":"晴天"}}]}`))...)
	out = append(out, enc.Feed([]byte(`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":13,"completion_tokens":5}}`))...)
	out = append(out, enc.Complete()...)
	if len(out) != 3 {
		t.Fatalf("帧数不符(两增量+一终帧): %d", len(out))
	}
	if !strings.Contains(string(out[0]), `{"text":"今天"}`) || !strings.Contains(string(out[1]), `{"text":"晴天"}`) {
		t.Fatalf("增量帧形态不符: %s %s", out[0], out[1])
	}
	final := string(out[2])
	for _, want := range []string{`"finishReason":"STOP"`, `"promptTokenCount":13`, `"candidatesTokenCount":5`, `"totalTokenCount":18`, `"role":"model"`} {
		if !strings.Contains(final, want) {
			t.Fatalf("终帧缺 %s: %s", want, final)
		}
	}
	// 断流收尾：Failed 产出 error 信封帧，不伪造正常终帧
	failed := NewGeminiStreamEncoder("m").Failed("上游流中断")
	if !strings.Contains(string(failed), `"error"`) || strings.Contains(string(failed), "finishReason") {
		t.Fatalf("失败帧形态不符: %s", failed)
	}
}

// TestGemini_回环对拍 出站翻译 ∘ 入站翻译 = 恒等，可翻译子集域，兼作语料漂移探测。
// 方向：gemini 语料请求 → 入站翻译 canonical → 模拟出站适配器同规则 → gemini 请求形态。
// 钉住协议层不变式：入站翻译的 canonical 经等价 chat 重放，消息序列、系统与工具语义不丢失。
func TestGemini_回环对拍(t *testing.T) {
	corpus := geminiCorpus(t)
	var req0 struct {
		PathModel string          `json:"path_model"`
		Body      json.RawMessage `json:"body"`
	}
	if err := json.Unmarshal(corpus["request"], &req0); err != nil {
		t.Fatal(err)
	}
	req, err := TranslateGeminiRequest(req0.PathModel, req0.Body, false)
	if err != nil {
		t.Fatal(err)
	}
	// canonical 语义域逐格，与语料请求同构：消息数 = system + user + model(tool) + user(tool) + user(text+image)
	var env struct {
		Messages []struct {
			Role      string `json:"role"`
			ToolCalls []any  `json:"tool_calls"`
		} `json:"messages"`
		Tools json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal(req.Raw, &env); err != nil {
		t.Fatal(err)
	}
	if len(env.Messages) != 5 {
		t.Fatalf("消息数不符(system+4 content): %d", len(env.Messages))
	}
	if env.Messages[2].Role != "assistant" || len(env.Messages[2].ToolCalls) != 1 {
		t.Fatalf("model content 应为 assistant+tool_calls: %+v", env.Messages[2])
	}
	if env.Messages[3].Role != "tool" || env.Messages[4].Role != "user" {
		t.Fatalf("消息序列不符: %+v", env.Messages)
	}
	if len(env.Tools) == 0 {
		t.Fatal("tools 不得丢失")
	}
}
