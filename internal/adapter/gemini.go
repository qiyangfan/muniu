// Gemini 将规范 chat 请求翻译为 Gemini REST API，并将响应、流和 usage 归一为 OpenAI 形态。
// 工具调用及 tool_choice 双向翻译；协议映射由 conformance fixture 覆盖。
package adapter

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/solvela/muniu/internal/failover"
	"github.com/solvela/muniu/internal/protocol"
)

// Gemini 保存 provider 的 API 基址。
type Gemini struct {
	baseURL string
}

// NewGemini 构造 Gemini 适配器。
func NewGemini(baseURL string) *Gemini {
	return &Gemini{baseURL: strings.TrimRight(baseURL, "/")}
}

// Prepare 构造 generateContent 请求；流式请求使用 streamGenerateContent，并通过请求头传递 API key。
func (g *Gemini) Prepare(in *protocol.ChatRequest, upstreamModel, apiKey string) (*Outbound, error) {
	body, err := g.buildRequest(in)
	if err != nil {
		return nil, fmt.Errorf("gemini: 翻译请求: %w", err)
	}
	method := ":generateContent"
	if in.Stream {
		method = ":streamGenerateContent?alt=sse"
	}
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("x-goog-api-key", apiKey)
	return &Outbound{
		URL:    g.baseURL + "/models/" + upstreamModel + method,
		Header: h,
		Body:   body,
	}, nil
}

// geminiWire 请求体形态；map 字段的键序由序列化决定，契约以 fixture 字段断言。
type geminiWire struct {
	Contents          []geminiWireContent `json:"contents"`
	SystemInstruction *geminiWireContent  `json:"systemInstruction,omitempty"`
	Tools             []map[string]any    `json:"tools,omitempty"`
	ToolConfig        map[string]any      `json:"toolConfig,omitempty"`
	GenerationConfig  map[string]any      `json:"generationConfig,omitempty"`
}

type geminiWireContent struct {
	Role  string           `json:"role"`
	Parts []map[string]any `json:"parts"`
}

// buildRequest 将规范 chat 请求转为 Gemini 请求体，包括 system、functionCall 和 functionResponse。
func (g *Gemini) buildRequest(in *protocol.ChatRequest) ([]byte, error) {
	var env struct {
		Messages []struct {
			Role      string          `json:"role"`
			Content   json.RawMessage `json:"content"`
			ToolCalls []struct {
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
			ToolCallID string `json:"tool_call_id"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(in.Raw, &env); err != nil {
		return nil, fmt.Errorf("canonical 消息解析失败: %w", err)
	}

	out := &geminiWire{}
	// top_p 在 ChatRequest 无类型化字段，从 Raw 层读取；anthropic 桥接同款处理
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(in.Raw, &fields)
	// tool_call_id 到 name 的回查表，依赖 assistant.tool_calls 先于 role=tool 到达
	callName := map[string]string{}

	for i := range env.Messages {
		m := &env.Messages[i]
		switch m.Role {
		case "system", "developer":
			if text := plainText(m.Content); text != "" {
				if out.SystemInstruction == nil {
					out.SystemInstruction = &geminiWireContent{Parts: []map[string]any{}}
				}
				out.SystemInstruction.Parts = append(out.SystemInstruction.Parts, map[string]any{"text": text})
			}
		case "user":
			c, err := g.userContent(m.Content)
			if err != nil {
				return nil, err
			}
			if c != nil {
				out.Contents = append(out.Contents, *c)
			}
		case "assistant":
			c := &geminiWireContent{Role: "model", Parts: []map[string]any{}}
			if text := plainText(m.Content); text != "" {
				c.Parts = append(c.Parts, map[string]any{"text": text})
			}
			for _, tc := range m.ToolCalls {
				callName[tc.ID] = tc.Function.Name
				args, err := jsonDecodeToAny(tc.Function.Arguments)
				if err != nil {
					return nil, fmt.Errorf("tool_calls[%s].arguments 非法 JSON: %w", tc.ID, err)
				}
				c.Parts = append(c.Parts, map[string]any{"functionCall": map[string]any{"name": tc.Function.Name, "args": args}})
			}
			if len(c.Parts) > 0 {
				out.Contents = append(out.Contents, *c)
			}
		case "tool":
			// Gemini functionResponse 归入 role=user 内容，name 由配对的 tool_calls 回查
			name, ok := callName[m.ToolCallID]
			if !ok {
				return nil, fmt.Errorf("role=tool 消息找不到配对的 tool_calls(id=%s)", m.ToolCallID)
			}
			response, err := jsonDecodeToAny(toolContentJSON(m.Content))
			if err != nil {
				return nil, fmt.Errorf("role=tool 消息 content 非法 JSON: %w", err)
			}
			out.Contents = append(out.Contents, geminiWireContent{Role: "user", Parts: []map[string]any{
				{"functionResponse": map[string]any{"name": name, "response": response}},
			}})
		default:
			continue // 未知角色不在桥接范围，丢弃；anthropic 桥接同款纪律
		}
	}
	if len(out.Contents) == 0 {
		return nil, fmt.Errorf("messages 为空或全部不可翻译")
	}

	if in.MaxTokens != nil && *in.MaxTokens > 0 {
		out.GenerationConfig = map[string]any{"maxOutputTokens": *in.MaxTokens}
	}
	if in.Temperature != nil {
		gencNotNil(&out.GenerationConfig)["temperature"] = *in.Temperature
	}
	if raw, ok := fields["top_p"]; ok {
		var topP float64
		if json.Unmarshal(raw, &topP) == nil {
			gencNotNil(&out.GenerationConfig)["topP"] = topP
		}
	}
	// canonical top_k 写入 generationConfig.topK，与 top_p 同款处理，避免 qwen 系参数静默丢失
	if raw, ok := fields["top_k"]; ok {
		var topK float64
		if json.Unmarshal(raw, &topK) == nil {
			gencNotNil(&out.GenerationConfig)["topK"] = topK
		}
	}
	if in.Tools != nil && string(in.Tools) != "null" {
		var oaiTools []struct {
			Type     string `json:"type"`
			Function struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				Parameters  json.RawMessage `json:"parameters"`
			} `json:"function"`
		}
		// 解析失败必须报错归 bad_request；静默吞掉会让上游在无工具状态下作答且无痕迹
		if err := json.Unmarshal(in.Tools, &oaiTools); err != nil {
			return nil, fmt.Errorf("tools 形态非法(须为 function 数组): %w", err)
		}
		var decls []map[string]any
		for _, tl := range oaiTools {
			if tl.Type != "function" || tl.Function.Name == "" {
				continue
			}
			decl := map[string]any{"name": tl.Function.Name}
			if tl.Function.Description != "" {
				decl["description"] = tl.Function.Description
			}
			if len(tl.Function.Parameters) > 0 && string(tl.Function.Parameters) != "null" {
				params, err := jsonDecodeToAny(string(tl.Function.Parameters))
				if err != nil {
					return nil, fmt.Errorf("tools[%s].parameters 非法 JSON: %w", tl.Function.Name, err)
				}
				decl["parameters"] = params
			}
			decls = append(decls, decl)
		}
		if len(decls) > 0 {
			out.Tools = []map[string]any{{"functionDeclarations": decls}}
		}
	}
	if raw, ok := fields["tool_choice"]; ok && len(raw) > 0 && string(raw) != "null" {
		if tc := g.toolChoiceOf(raw); tc != nil {
			out.ToolConfig = map[string]any{"functionCallingConfig": tc}
		}
	}
	return json.Marshal(out)
}

// gencNotNil 惰性初始化 generationConfig，仅存在非缺省参数时才出现在 wire。
func gencNotNil(m *map[string]any) map[string]any {
	if *m == nil {
		*m = map[string]any{}
	}
	return *m
}

// toolChoiceOf 将 chat tool_choice 映射为 functionCallingConfig：
// auto→AUTO，required→ANY，具名→ANY 加 allowedFunctionNames，none→NONE。
func (g *Gemini) toolChoiceOf(raw json.RawMessage) map[string]any {
	var tc any
	if json.Unmarshal(raw, &tc) != nil {
		return nil
	}
	switch v := tc.(type) {
	case string:
		switch v {
		case "auto":
			return map[string]any{"mode": "AUTO"}
		case "required":
			return map[string]any{"mode": "ANY"}
		case "none":
			return map[string]any{"mode": "NONE"}
		}
	case map[string]any:
		if name, ok := v["function"].(map[string]any)["name"].(string); ok && name != "" {
			return map[string]any{"mode": "ANY", "allowedFunctionNames": []string{name}}
		}
	}
	return nil
}

// userContent 将 user 消息转为 content：文本与 data URL 图片映射 inlineData，http(s) URL 映射 fileData。
func (g *Gemini) userContent(raw json.RawMessage) (*geminiWireContent, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if s == "" {
			return nil, nil
		}
		return &geminiWireContent{Role: "user", Parts: []map[string]any{{"text": s}}}, nil
	}
	var parts []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		ImageURL struct {
			URL string `json:"url"`
		} `json:"image_url"`
	}
	if err := json.Unmarshal(raw, &parts); err != nil {
		return nil, fmt.Errorf("user content 形态非法: %w", err)
	}
	out := &geminiWireContent{Role: "user", Parts: []map[string]any{}}
	for _, p := range parts {
		switch {
		case p.Text != "":
			out.Parts = append(out.Parts, map[string]any{"text": p.Text})
		case p.ImageURL.URL != "":
			const dataPrefix = "data:"
			if strings.HasPrefix(p.ImageURL.URL, dataPrefix) {
				mime, b64, ok := parseDataURL(p.ImageURL.URL)
				if !ok {
					return nil, fmt.Errorf("image_url data URL 形态非法: %q", truncateURL(p.ImageURL.URL))
				}
				out.Parts = append(out.Parts, map[string]any{"inlineData": map[string]any{"mimeType": mime, "data": b64}})
			} else {
				// http(s) URL 映射 fileData{fileUri}，文件拉取由上游负责
				out.Parts = append(out.Parts, map[string]any{"fileData": map[string]any{"fileUri": p.ImageURL.URL}})
			}
		}
	}
	if len(out.Parts) == 0 {
		return nil, nil
	}
	return out, nil
}

// parseDataURL 解析 data:mime;base64,payload，返回 mime、payload 和是否成功。
func parseDataURL(u string) (string, string, bool) {
	rest := strings.TrimPrefix(u, "data:")
	semi := strings.Index(rest, ";base64,")
	if semi < 0 {
		return "", "", false
	}
	return rest[:semi], rest[semi+len(";base64,"):], true
}

// truncateURL 日志用 URL 截断。
func truncateURL(u string) string {
	if len(u) > 64 {
		return u[:64] + "…"
	}
	return u
}

// jsonDecodeToAny 将原始 JSON 解码为 any，空或 null 时返回空对象。
func jsonDecodeToAny(raw string) (any, error) {
	if raw == "" || raw == "null" {
		return map[string]any{}, nil
	}
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return nil, err
	}
	return v, nil
}

// toolContentJSON 将 role=tool 消息 content 转为 JSON 对象字面量；字符串包装为对象形态，
// 与 Gemini functionResponse.response 的对象语义对齐。
func toolContentJSON(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return "{}"
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		b, _ := json.Marshal(map[string]any{"result": s})
		return string(b)
	}
	return string(raw)
}

// plainText 抽取消息 content 的文本：字符串原样返回，结构体取 text 部件拼接。
func plainText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		var sb []string
		for _, p := range parts {
			if p.Text != "" {
				sb = append(sb, p.Text)
			}
		}
		return strings.Join(sb, "\n")
	}
	return ""
}

// Classify 按状态码与 body 内 error.status 字面量归类：
// 400/INVALID_ARGUMENT 归 bad_request；401/403 归 auth；429/RESOURCE_EXHAUSTED 归限流；
// 5xx/UNAVAILABLE/DEADLINE_EXCEEDED 归 server_error；其余归 unknown。
func (g *Gemini) Classify(status int, body []byte) failover.ErrorClass {
	var probe struct {
		Error struct {
			Code   int    `json:"code"`
			Status string `json:"status"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &probe)
	glStatus := probe.Error.Status
	switch {
	case status == http.StatusBadRequest, glStatus == "INVALID_ARGUMENT":
		return failover.ClassBadRequest
	case status == http.StatusUnauthorized, status == http.StatusForbidden:
		return failover.ClassAuthFailed
	case status == http.StatusTooManyRequests, glStatus == "RESOURCE_EXHAUSTED":
		return failover.ClassRateLimited
	case status >= 500, glStatus == "UNAVAILABLE", glStatus == "DEADLINE_EXCEEDED":
		return failover.ClassUpstream5xx
	default:
		return failover.ClassUnknown
	}
}

// geminiUsageMetadata 是 usageMetadata 原生形态；thoughtsTokenCount 归 reasoning 口径，
// reasoning 为 completion 子集，不与 candidates 相加。
type geminiUsageMetadata struct {
	PromptTokenCount        int64 `json:"promptTokenCount"`
	CandidatesTokenCount    int64 `json:"candidatesTokenCount"`
	CachedContentTokenCount int64 `json:"cachedContentTokenCount"`
	ThoughtsTokenCount      int64 `json:"thoughtsTokenCount"`
}

// NormalizeUsage 归一 usage，兼容两种形态：上游原生 usageMetadata 键，以及流式翻译
// chunk 内嵌的统一形态键。
func (g *Gemini) NormalizeUsage(raw json.RawMessage) (protocol.Usage, error) {
	var m geminiUsageMetadata
	if err := json.Unmarshal(raw, &m); err == nil && m.PromptTokenCount != 0 {
		return protocol.Usage{
			PromptTokens:     m.PromptTokenCount,
			CompletionTokens: m.CandidatesTokenCount,
			CachedTokens:     m.CachedContentTokenCount,
			ReasoningTokens:  m.ThoughtsTokenCount,
		}, nil
	}
	return NewOpenAI("", "").NormalizeUsage(raw)
}

// StreamEvent 将 Gemini SSE 帧翻译为 OpenAI chunk：
// text/thought → content/reasoning_content；functionCall → tool_calls 首 delta（args 单帧完整）；
// finishReason → finish_reason 加 usage；{error:{...}} 交由 proxy 归类换道。
// 流无 [DONE]，关流即收尾；本方法为无状态帧翻译，跨帧工具序号由 NewStreamSequencer 承担。
func (g *Gemini) StreamEvent(data []byte) ([]byte, EventMeta, error) {
	return g.streamEvent(data, nil, nil)
}

// NewStreamSequencer 创建每流测序器：Gemini wire 不携带工具调用序号，跨帧并行 functionCall
// 按流计数合成 index；同名调用 id 按序去重（call_<name>、call_<name>_N），与入站翻译同规。
func (g *Gemini) NewStreamSequencer() func([]byte) ([]byte, EventMeta, error) {
	seq := &geminiStreamSeq{g: g, callSeq: map[string]int{}}
	return seq.event
}

type geminiStreamSeq struct {
	g       *Gemini
	toolIdx int            // 已发出的 functionCall 跨帧总序
	callSeq map[string]int // 同名调用去重计数
}

func (s *geminiStreamSeq) event(data []byte) ([]byte, EventMeta, error) {
	return s.g.streamEvent(data, &s.toolIdx, s.callSeq)
}

// streamEvent 帧翻译核心：测序态跨帧连续计数并同名去重，无状态态按帧内计数。
// 一帧聚合为一个 delta，文本、思考与工具调用可并存，finish_reason 可与内容同帧；
// 逐对象裸拼接会产出非法 SSE data 行。
func (g *Gemini) streamEvent(data []byte, toolIdx *int, callSeq map[string]int) ([]byte, EventMeta, error) {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || strings.HasPrefix(trimmed, ":") {
		return nil, EventMeta{}, nil // 心跳/注释帧，不可见
	}
	if trimmed == "[DONE]" {
		return data, EventMeta{IsDone: true}, nil // 兼容性防御：真 [DONE] 到达按既有语义收尾
	}
	var frame struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text         string `json:"text"`
					Thought      bool   `json:"thought"`
					FunctionCall *struct {
						Name string          `json:"name"`
						Args json.RawMessage `json:"args"`
					} `json:"functionCall"`
				} `json:"parts"`
			} `json:"content"`
			FinishReason string `json:"finishReason"`
		} `json:"candidates"`
		UsageMetadata *geminiUsageMetadata `json:"usageMetadata"`
		Error         *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &frame); err != nil {
		return nil, EventMeta{}, fmt.Errorf("gemini: 解析流帧: %w", err)
	}
	if frame.Error != nil {
		return nil, EventMeta{}, fmt.Errorf("gemini: 流内错误帧 status=%s", frame.Error.Status)
	}
	hasContent := false
	frameCalls := 0 // 帧内 functionCall 序，无状态形态的局部计数
	var reasoning, content strings.Builder
	var toolDeltas []string
	finish := ""
	usagePart := ""
	for _, c := range frame.Candidates {
		for _, p := range c.Content.Parts {
			switch {
			case p.Text != "" && p.Thought:
				reasoning.WriteString(p.Text)
				hasContent = true
			case p.Text != "":
				content.WriteString(p.Text)
				hasContent = true
			case p.FunctionCall != nil:
				args := string(p.FunctionCall.Args)
				if args == "" || args == "null" {
					args = "{}"
				}
				// index/id 合成：每流测序按流计数并同名去重；无状态按帧内序，id 维持既有约定
				idx, id := frameCalls, "call_"+p.FunctionCall.Name
				if callSeq != nil {
					callSeq[p.FunctionCall.Name]++
					if n := callSeq[p.FunctionCall.Name]; n > 1 {
						id = fmt.Sprintf("%s_%d", id, n)
					}
					idx = *toolIdx
					*toolIdx++
				}
				frameCalls++
				// id 整体经 JSON 编码：名字含引号或反斜杠时裸拼接会产出非法 JSON 帧
				toolDeltas = append(toolDeltas, `{"index":`+itoa64(int64(idx))+`,"id":`+mustJSONStrGemini(id)+
					`,"type":"function","function":{"name":`+mustJSONStrGemini(p.FunctionCall.Name)+
					`,"arguments":`+mustJSONStrGemini(args)+`}}`)
				hasContent = true
			}
		}
		if c.FinishReason != "" {
			finish = c.FinishReason
			if frame.UsageMetadata != nil {
				// 统一形态键名；usageMetadata 是上游原生键，不进 chunk
				u := frame.UsageMetadata
				usagePart = `,"usage":{"prompt_tokens":` + itoa64(u.PromptTokenCount) +
					`,"completion_tokens":` + itoa64(u.CandidatesTokenCount) +
					`,"cached_tokens":` + itoa64(u.CachedContentTokenCount) +
					`,"reasoning_tokens":` + itoa64(u.ThoughtsTokenCount) + `}`
			}
		}
	}
	var payload strings.Builder
	if reasoning.Len() > 0 || content.Len() > 0 || len(toolDeltas) > 0 {
		var fields []string
		if reasoning.Len() > 0 {
			fields = append(fields, `"reasoning_content":`+mustJSONStrGemini(reasoning.String()))
		}
		if content.Len() > 0 {
			fields = append(fields, `"content":`+mustJSONStrGemini(content.String()))
		}
		if len(toolDeltas) > 0 {
			fields = append(fields, `"tool_calls":[`+strings.Join(toolDeltas, ",")+`]`)
		}
		if finish != "" {
			payload.WriteString(`{"choices":[{"index":0,"delta":{` + strings.Join(fields, ",") +
				`},"finish_reason":` + mustJSONStrGemini(g.finishToChat(finish)) + `}]` + usagePart + `}`)
		} else {
			payload.WriteString(`{"choices":[{"index":0,"delta":{` + strings.Join(fields, ",") + `}}]}`)
		}
	} else if finish != "" {
		payload.WriteString(`{"choices":[{"index":0,"delta":{},"finish_reason":` +
			mustJSONStrGemini(g.finishToChat(finish)) + `}]` + usagePart + `}`)
	}
	if payload.Len() == 0 {
		// 纯 usage 帧或空 candidates：usage 单独成帧供计量，统一形态键名
		if frame.UsageMetadata != nil {
			u := frame.UsageMetadata
			return []byte(`{"choices":[],"usage":{"prompt_tokens":` + itoa64(u.PromptTokenCount) +
				`,"completion_tokens":` + itoa64(u.CandidatesTokenCount) +
				`,"cached_tokens":` + itoa64(u.CachedContentTokenCount) +
				`,"reasoning_tokens":` + itoa64(u.ThoughtsTokenCount) + `}}`), EventMeta{IsUsage: true}, nil
		}
		return nil, EventMeta{}, nil
	}
	return []byte(payload.String()), EventMeta{HasContent: hasContent, IsUsage: frame.UsageMetadata != nil}, nil
}

// finishToChat 将 Gemini finishReason 映射为 chat finish_reason；未知值兜底 stop。
func (g *Gemini) finishToChat(f string) string {
	switch f {
	case "MAX_TOKENS":
		return "length"
	case "SAFETY", "RECITATION", "PROHIBITED_CONTENT", "BLOCKLIST", "SPII":
		return "content_filter"
	default: // STOP 及未知新值，含工具调用收束
		return "stop"
	}
}

// RewriteResponse 将非流式 GenerateContentResponse 转为 OpenAI ChatResponse。
// promptFeedback.blockReason 非空时报错归 content_filter，禁止以空内容伪造成功。
func (g *Gemini) RewriteResponse(body []byte) ([]byte, json.RawMessage, error) {
	var resp struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text         string `json:"text"`
					Thought      bool   `json:"thought"`
					FunctionCall *struct {
						Name string          `json:"name"`
						Args json.RawMessage `json:"args"`
					} `json:"functionCall"`
				} `json:"parts"`
			} `json:"content"`
			FinishReason string `json:"finishReason"`
		} `json:"candidates"`
		PromptFeedback *struct {
			BlockReason string `json:"blockReason"`
		} `json:"promptFeedback"`
		UsageMetadata *geminiUsageMetadata `json:"usageMetadata"`
		ModelVersion  string               `json:"modelVersion"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, nil, fmt.Errorf("gemini: 解析响应: %w", err)
	}
	if resp.PromptFeedback != nil && resp.PromptFeedback.BlockReason != "" {
		return nil, nil, fmt.Errorf("gemini: prompt 被安全策略拦截(blockReason=%s)", resp.PromptFeedback.BlockReason)
	}
	content := ""
	var reasoning strings.Builder
	type toolCall struct {
		Index    int    `json:"index"`
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	}
	var calls []toolCall
	callSeq := map[string]int{} // 同名并行调用 id 去重，与流式测序和入站翻译同规
	finish := ""
	for i, c := range resp.Candidates {
		if i == 0 {
			finish = g.finishToChat(c.FinishReason)
		}
		for _, p := range c.Content.Parts {
			switch {
			case p.Text != "" && p.Thought:
				reasoning.WriteString(p.Text)
			case p.Text != "":
				content += p.Text
			case p.FunctionCall != nil:
				args := string(p.FunctionCall.Args)
				if args == "" || args == "null" {
					args = "{}"
				}
				callSeq[p.FunctionCall.Name]++
				id := "call_" + p.FunctionCall.Name
				if n := callSeq[p.FunctionCall.Name]; n > 1 {
					id = fmt.Sprintf("%s_%d", id, n)
				}
				calls = append(calls, toolCall{
					Index: len(calls), ID: id, Type: "function",
					Function: struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					}{Name: p.FunctionCall.Name, Arguments: args},
				})
			}
		}
	}
	msg := map[string]any{"role": "assistant", "content": content}
	if reasoning.Len() > 0 {
		msg["reasoning_content"] = reasoning.String()
	}
	if len(calls) > 0 {
		msg["tool_calls"] = calls
	}
	usage := map[string]any{}
	if resp.UsageMetadata != nil {
		m := resp.UsageMetadata
		usage = map[string]any{
			"prompt_tokens":             m.PromptTokenCount,
			"completion_tokens":         m.CandidatesTokenCount,
			"cached_tokens":             m.CachedContentTokenCount,
			"prompt_tokens_details":     map[string]any{"cached_tokens": m.CachedContentTokenCount},
			"completion_tokens_details": map[string]any{"reasoning_tokens": m.ThoughtsTokenCount},
		}
	}
	model := resp.ModelVersion
	out := map[string]any{
		"id": model + "-resp", "model": model, "object": "chat.completion",
		"choices": []any{map[string]any{"index": 0, "message": msg, "finish_reason": finish}},
		"usage":   usage,
	}
	outBytes, err := json.Marshal(out)
	if err != nil {
		return nil, nil, err
	}
	usageBytes, _ := json.Marshal(usage)
	return outBytes, usageBytes, nil
}

// BridgeKind 标记 Gemini 为真异构协议桥接，翻译为 OpenAI 形态。
func (g *Gemini) BridgeKind() string { return "gemini" }

// ToolRoundtripComplete 声明工具多轮请求侧翻译完整，functionCall/functionResponse 双向支持。
func (g *Gemini) ToolRoundtripComplete() bool { return true }

// StreamCloseIsDone 表示 Gemini 流无 [DONE] 终止标记，干净关流即正常收尾。
func (g *Gemini) StreamCloseIsDone() bool { return true }

// itoa64 极小整数转字符串，供 usage chunk 内联构造使用。
func itoa64(n int64) string { return strconv.FormatInt(n, 10) }

// mustJSONStrGemini 极小封装，与 bridge 包同款，包内独立持有。
func mustJSONStrGemini(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(b)
}
