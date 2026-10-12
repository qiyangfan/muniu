// Gemini 原生协议双向翻译(gemini-native-protocol;契约=proxy-and-streaming「Gemini 入站翻译」
// 与 upstream-adapters Gemini 归一表):入站 contents→chat 消息序列、响应/流反向映射为
// GenerateContentResponse。翻译失败以 GeminiUnsupportedError 携带机器码,handler 映射 400
// Gemini 错误信封;回环对拍(出站翻译 ∘ 入站翻译 = 恒等,可翻译子集域)由 conformance 语料钉死。
package protocol

import (
	"encoding/json"
	"fmt"
	"strings"
)

// 显式拒绝机器码(handler 映射 400 Gemini 信封;禁散落硬编码)。
const (
	CodeGeminiContentPartUnsupported  = "content_part_unsupported"
	CodeGeminiStreamFormUnsupported   = "stream_form_unsupported"
	CodeGeminiInputInvalid            = "input_invalid"
	CodeGeminiModelNotFound           = "model_not_found"
	CodeGeminiToolUnsupported         = "tool_unsupported"
	CodeGeminiPreviousIDNotApplicable = "previous_id_not_applicable"
)

// GeminiUnsupportedError 显式拒绝(不支持项/非法请求);handler 映射 400 + Gemini 信封。
type GeminiUnsupportedError struct {
	Code   string // 机器码(muniu 追责口径)
	Status string // Gemini status 字面量(INVALID_ARGUMENT 等,SDK 消费)
}

func (e *GeminiUnsupportedError) Error() string { return "gemini: " + e.Code }

func geminiInvalid() *GeminiUnsupportedError {
	return &GeminiUnsupportedError{Code: CodeGeminiInputInvalid, Status: "INVALID_ARGUMENT"}
}

// ---- 请求侧 DTO(入站) ----

type geminiPart struct {
	Text       string `json:"text"`
	Thought    bool   `json:"thought"`
	InlineData *struct {
		MimeType string `json:"mimeType"`
		Data     string `json:"data"`
	} `json:"inlineData"`
	FileData *struct {
		MimeType string `json:"mimeType"`
		FileUri  string `json:"fileUri"`
	} `json:"fileData"`
	FunctionCall *struct {
		Name string          `json:"name"`
		Args json.RawMessage `json:"args"`
	} `json:"functionCall"`
	FunctionResponse *struct {
		Name     string          `json:"name"`
		Response json.RawMessage `json:"response"`
	} `json:"functionResponse"`
}

type geminiContent struct {
	Role  string       `json:"role"` // user | model
	Parts []geminiPart `json:"parts"`
}

type geminiTool struct {
	FunctionDeclarations []struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"functionDeclarations"`
}

type geminiGenConfig struct {
	Temperature     *float64 `json:"temperature"`
	TopP            *float64 `json:"topP"`
	MaxOutputTokens *int64   `json:"maxOutputTokens"`
	StopSequences   []string `json:"stopSequences"`
}

type geminiRequest struct {
	Contents          []geminiContent `json:"contents"`
	SystemInstruction json.RawMessage `json:"systemInstruction"` // 字符串或 {parts:[...]} 两形态
	Tools             []geminiTool    `json:"tools"`
	ToolConfig        json.RawMessage `json:"toolConfig"` // {functionCallingConfig:{mode,allowedFunctionNames}}
	GenerationConfig  geminiGenConfig `json:"generationConfig"`
}

// TranslateGeminiRequest Gemini generateContent 请求 → 规范 chat 请求(模型别名取自路径;
// stream 由 handler 依 URL 方法注入——Gemini wire 无 stream 字段,流式语义在端点本身)。
// 返回的 ChatRequest.Raw 即 canonical chat JSON 字节——治理/缓存/计量与等价 chat 请求同构。
func TranslateGeminiRequest(pathModel string, body []byte, stream bool) (*ChatRequest, error) {
	if pathModel == "" {
		return nil, geminiInvalid()
	}
	var gr geminiRequest
	if err := json.Unmarshal(body, &gr); err != nil {
		return nil, geminiInvalid()
	}
	if len(gr.Contents) == 0 {
		return nil, geminiInvalid()
	}

	var b strings.Builder
	b.WriteString(`{"model":`)
	b.Write(mustJSON(pathModel))

	var msgs []string
	// systemInstruction:字符串快捷形态或 {parts:[...]} 块形态 → 单条 system 消息
	if len(gr.SystemInstruction) > 0 && string(gr.SystemInstruction) != "null" {
		sys, err := geminiSystemToText(gr.SystemInstruction)
		if err != nil {
			return nil, err
		}
		if sys != "" {
			msgs = append(msgs, `{"role":"system","content":`+string(mustJSON(sys))+`}`)
		}
	}
	// functionCall 合成 id 登记(同名调用按序去重;并行同名调用的 id 配对属登记限制)
	callSeq := map[string]int{}
	lastCallID := map[string]string{}
	for i := range gr.Contents {
		c := &gr.Contents[i]
		role := ""
		switch c.Role {
		case "user":
			role = "user"
		case "model":
			role = "assistant"
		default:
			return nil, geminiInvalid()
		}
		out, err := geminiPartsToChat(role, c.Parts, callSeq, lastCallID)
		if err != nil {
			return nil, err
		}
		msgs = append(msgs, out...)
	}
	b.WriteString(`,"messages":[` + strings.Join(msgs, ",") + `]`)
	if stream {
		b.WriteString(`,"stream":true`)
	}
	if gr.GenerationConfig.Temperature != nil {
		b.WriteString(`,"temperature":` + fmt.Sprint(*gr.GenerationConfig.Temperature))
	}
	if gr.GenerationConfig.TopP != nil {
		b.WriteString(`,"top_p":` + fmt.Sprint(*gr.GenerationConfig.TopP))
	}
	if gr.GenerationConfig.MaxOutputTokens != nil && *gr.GenerationConfig.MaxOutputTokens > 0 {
		b.WriteString(`,"max_tokens":` + fmt.Sprint(*gr.GenerationConfig.MaxOutputTokens))
	}
	if len(gr.GenerationConfig.StopSequences) > 0 {
		tj, _ := json.Marshal(gr.GenerationConfig.StopSequences)
		b.WriteString(`,"stop":` + string(tj))
	}
	if len(gr.Tools) > 0 {
		out := make([]map[string]any, 0, len(gr.Tools))
		for _, tl := range gr.Tools {
			for _, fd := range tl.FunctionDeclarations {
				fn := map[string]any{"name": fd.Name}
				if fd.Description != "" {
					fn["description"] = fd.Description
				}
				if len(fd.Parameters) > 0 && string(fd.Parameters) != "null" {
					var schema any
					if json.Unmarshal(fd.Parameters, &schema) != nil {
						return nil, geminiInvalid()
					}
					fn["parameters"] = schema
				}
				out = append(out, map[string]any{"type": "function", "function": fn})
			}
		}
		if len(out) > 0 {
			tj, _ := json.Marshal(out)
			b.WriteString(`,"tools":` + string(tj))
		}
	}
	if len(gr.ToolConfig) > 0 && string(gr.ToolConfig) != "null" {
		tc, err := translateGeminiToolConfig(gr.ToolConfig)
		if err != nil {
			return nil, err
		}
		if tc != "" {
			b.WriteString(`,"tool_choice":` + tc)
		}
	}
	b.WriteString(`}`)

	// canonical JSON 经既有解析器回填 typed 字段(单源,避免两套解析)
	return ParseChatRequest([]byte(b.String()))
}

// geminiSystemToText systemInstruction → 文本(字符串快捷形态或 text 部件块)。
func geminiSystemToText(raw json.RawMessage) (string, error) {
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return "", geminiInvalid()
		}
		return s, nil
	}
	var wrapper struct {
		Parts []geminiPart `json:"parts"`
	}
	if json.Unmarshal(raw, &wrapper) != nil {
		return "", geminiInvalid()
	}
	var parts []string
	for _, p := range wrapper.Parts {
		if p.Text == "" {
			return "", &GeminiUnsupportedError{Code: CodeGeminiContentPartUnsupported, Status: "INVALID_ARGUMENT"}
		}
		parts = append(parts, p.Text)
	}
	return strings.Join(parts, "\n"), nil
}

// geminiPartsToChat 单 content 的 parts → 0..n 条 chat 消息:
//   - 纯 text(+inlineData image)→ 同角色消息(多模态经 image_url data URL 承载);
//   - assistant 的 functionCall → assistant(tool_calls) 消息(id 按名合成,同名按序去重);
//   - user 的 functionResponse → role:tool 消息(tool_call_id 回指同名最近 functionCall)。
func geminiPartsToChat(role string, parts []geminiPart, callSeq map[string]int, lastCallID map[string]string) ([]string, error) {
	var textParts []string
	var imageURLs []string
	var toolCalls []string
	var msgs []string
	flushText := func() {
		if len(textParts) == 0 && len(imageURLs) == 0 {
			return
		}
		if len(imageURLs) == 0 {
			msgs = append(msgs, `{"role":`+string(mustJSON(role))+`,"content":`+string(mustJSON(strings.Join(textParts, "\n")))+`}`)
		} else {
			// 多模态:canonical image_url 形态(data URL 由 inlineData 合成)
			content := `[`
			for i, u := range imageURLs {
				if i > 0 {
					content += `,`
				}
				content += `{"type":"image_url","image_url":{"url":` + string(mustJSON(u)) + `}}`
			}
			if len(textParts) > 0 {
				content += `,{"type":"text","text":` + string(mustJSON(strings.Join(textParts, "\n"))) + `}`
			}
			content += `]`
			msgs = append(msgs, `{"role":`+string(mustJSON(role))+`,"content":`+content+`}`)
		}
		textParts, imageURLs = nil, nil
	}
	for _, p := range parts {
		// 部件字段顺序处理(text 与 inlineData 可并存于同一部件;函数部件互斥)
		if p.Text != "" {
			textParts = append(textParts, p.Text)
		}
		if p.InlineData != nil {
			if !strings.HasPrefix(p.InlineData.MimeType, "image/") {
				return nil, &GeminiUnsupportedError{Code: CodeGeminiContentPartUnsupported, Status: "INVALID_ARGUMENT"}
			}
			imageURLs = append(imageURLs, "data:"+p.InlineData.MimeType+";base64,"+p.InlineData.Data)
		}
		if p.FileData != nil {
			return nil, &GeminiUnsupportedError{Code: CodeGeminiContentPartUnsupported, Status: "INVALID_ARGUMENT"}
		}
		if p.FunctionCall != nil {
			flushText()
			args := "{}"
			if len(p.FunctionCall.Args) > 0 && string(p.FunctionCall.Args) != "null" {
				args = string(p.FunctionCall.Args)
			}
			name := p.FunctionCall.Name
			callSeq[name]++
			id := "call_" + name
			if callSeq[name] > 1 {
				id = fmt.Sprintf("%s_%d", id, callSeq[name])
			}
			lastCallID[name] = id
			toolCalls = append(toolCalls, `{"id":`+string(mustJSON(id))+`,"type":"function","function":{"name":`+string(mustJSON(name))+`,"arguments":`+string(mustJSON(args))+`}}`)
		}
		if p.FunctionResponse != nil {
			flushText()
			id, ok := lastCallID[p.FunctionResponse.Name]
			if !ok {
				return nil, geminiInvalid() // response 先于 call:非法序列
			}
			content := "{}"
			if len(p.FunctionResponse.Response) > 0 && string(p.FunctionResponse.Response) != "null" {
				content = string(p.FunctionResponse.Response)
			}
			msgs = append(msgs, `{"role":"tool","tool_call_id":`+string(mustJSON(id))+`,"content":`+string(mustJSON(content))+`}`)
		}
		// thought 请求部件:丢弃(签名回传语义不成立,登记限制)
	}
	if len(toolCalls) > 0 {
		content := "null"
		if len(textParts) > 0 {
			content = string(mustJSON(strings.Join(textParts, "\n")))
		}
		msgs = append(msgs, `{"role":`+string(mustJSON(role))+`,"content":`+content+`,"tool_calls":[`+strings.Join(toolCalls, ",")+`]}`)
		return msgs, nil
	}
	flushText()
	return msgs, nil
}

// translateGeminiToolConfig functionCallingConfig.mode → chat tool_choice(完整映射含 NONE)。
// allowedFunctionNames 单元素 → 具名形态;多元素/无 mode 无名单 → 静默丢弃(登记限制)。
func translateGeminiToolConfig(raw json.RawMessage) (string, error) {
	var cfg struct {
		FunctionCallingConfig struct {
			Mode                 string   `json:"mode"`
			AllowedFunctionNames []string `json:"allowedFunctionNames"`
		} `json:"functionCallingConfig"`
	}
	if json.Unmarshal(raw, &cfg) != nil {
		return "", geminiInvalid()
	}
	switch cfg.FunctionCallingConfig.Mode {
	case "AUTO":
		return `"auto"`, nil
	case "ANY":
		if len(cfg.FunctionCallingConfig.AllowedFunctionNames) == 1 {
			return `{"type":"function","function":{"name":` + string(mustJSON(cfg.FunctionCallingConfig.AllowedFunctionNames[0])) + `}}`, nil
		}
		return `"required"`, nil
	case "NONE":
		return `"none"`, nil
	case "":
		return "", nil // 未声明 mode:不映射(上游缺省行为)
	default:
		return "", geminiInvalid()
	}
}

// ---- 响应侧映射(入站反向映射:canonical chat → Gemini 形态) ----

// geminiFinishReasonFromChat chat finish_reason → Gemini finishReason(冻结映射表;
// 未知值兜底 STOP,与出站适配器映射互为镜像)。
func geminiFinishReasonFromChat(finish string) string {
	switch finish {
	case "length":
		return "MAX_TOKENS"
	case "content_filter":
		return "SAFETY"
	default: // stop(含 tool_calls:Gemini 以 STOP 收束工具调用)
		return "STOP"
	}
}

// GenerateContentResponseFromChat 非流式:chat.completion → GenerateContentResponse。
// reasoning_content → thought 部件(展示形态,签名回传语义不成立,登记限制);
// tool_calls → functionCall 部件(args 反序列化为对象形态)。
func GenerateContentResponseFromChat(chatBody []byte, modelVersion string) ([]byte, error) {
	var chat struct {
		Choices []struct {
			Message struct {
				Content          *string `json:"content"`
				ReasoningContent *string `json:"reasoning_content"`
				ToolCalls        []struct {
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage *Usage `json:"usage"`
	}
	if err := json.Unmarshal(chatBody, &chat); err != nil {
		return nil, err
	}
	var parts []map[string]any
	finish := ""
	if len(chat.Choices) > 0 {
		ch := chat.Choices[0]
		finish = ch.FinishReason
		if ch.Message.ReasoningContent != nil && *ch.Message.ReasoningContent != "" {
			parts = append(parts, map[string]any{"text": *ch.Message.ReasoningContent, "thought": true})
		}
		if ch.Message.Content != nil && *ch.Message.Content != "" {
			parts = append(parts, map[string]any{"text": *ch.Message.Content})
		}
		for _, tc := range ch.Message.ToolCalls {
			var args any
			if json.Unmarshal([]byte(tc.Function.Arguments), &args) != nil || tc.Function.Arguments == "" {
				args = map[string]any{}
			}
			parts = append(parts, map[string]any{"functionCall": map[string]any{"name": tc.Function.Name, "args": args}})
		}
	}
	candidate := map[string]any{
		"content":      map[string]any{"parts": parts, "role": "model"},
		"finishReason": geminiFinishReasonFromChat(finish),
		"index":        0,
	}
	resp := map[string]any{
		"candidates":   []any{candidate},
		"modelVersion": modelVersion,
	}
	if chat.Usage != nil {
		resp["usageMetadata"] = geminiUsage(map[string]int64{
			"prompt": chat.Usage.PromptTokens, "completion": chat.Usage.CompletionTokens,
			"cached": chat.Usage.CachedTokens, "thoughts": chat.Usage.ReasoningTokens,
		})
	}
	return json.Marshal(resp)
}

// geminiUsage 统一口径 → usageMetadata(total=prompt+completion;cached/thoughts 非零才带)。
func geminiUsage(u map[string]int64) map[string]any {
	out := map[string]any{
		"promptTokenCount":     u["prompt"],
		"candidatesTokenCount": u["completion"],
		"totalTokenCount":      u["prompt"] + u["completion"],
	}
	if u["cached"] > 0 {
		out["cachedContentTokenCount"] = u["cached"]
	}
	if u["thoughts"] > 0 {
		out["thoughtsTokenCount"] = u["thoughts"]
	}
	return out
}

// ---- 流式事件编码器(上游 OpenAI chunk → alt=sse Gemini 帧) ----

// GeminiStreamEncoder 把上游 OpenAI 形态 SSE 帧翻译为 Gemini 流帧
// (顺序不变式:内容增量帧在前,携带 finishReason+usageMetadata 的终帧收尾;
// 工具调用按收束帧发出——Gemini functionCall 部件为完整对象,无增量形态,登记限制)。
// 非并发安全:单请求单实例。
type GeminiStreamEncoder struct {
	modelVersion string
	usage        *Usage
	finish       string
	toolArgs     map[int]*strings.Builder
	toolNames    map[int]string
	toolOrder    []int
	done         bool
}

// NewGeminiStreamEncoder 构造;modelVersion 回显给客户端(展示用)。
func NewGeminiStreamEncoder(modelVersion string) *GeminiStreamEncoder {
	return &GeminiStreamEncoder{
		modelVersion: modelVersion,
		toolArgs:     map[int]*strings.Builder{},
		toolNames:    map[int]string{},
	}
}

func (e *GeminiStreamEncoder) event(v map[string]any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// geminiDeltaFrame 内容增量帧。
func (e *GeminiStreamEncoder) geminiDeltaFrame(parts []map[string]any) []byte {
	return e.event(map[string]any{"candidates": []any{map[string]any{
		"content": map[string]any{"parts": parts, "role": "model"}, "index": 0,
	}}})
}

// Feed 消费一帧上游 OpenAI chunk,返回待写的 Gemini 流帧(可空)。
func (e *GeminiStreamEncoder) Feed(chunk []byte) [][]byte {
	c, err := ParseStreamChunk(chunk)
	if err != nil {
		return nil
	}
	if e.done {
		return nil
	}
	var out [][]byte
	for _, ch := range c.Choices {
		if ch.Index != nil && *ch.Index != 0 {
			continue // 仅映射首 choice(防御多 choice 合流错乱)
		}
		var deltaParts []map[string]any
		if ch.Delta.ReasoningContent != nil && *ch.Delta.ReasoningContent != "" {
			deltaParts = append(deltaParts, map[string]any{"text": *ch.Delta.ReasoningContent, "thought": true})
		}
		if ch.Delta.Content != nil && *ch.Delta.Content != "" {
			deltaParts = append(deltaParts, map[string]any{"text": *ch.Delta.Content})
		}
		if len(deltaParts) > 0 {
			out = append(out, e.geminiDeltaFrame(deltaParts))
		}
		for _, raw := range ch.Delta.ToolCalls {
			var tc struct {
				Index    *int `json:"index"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			}
			_ = json.Unmarshal(raw, &tc)
			idx := 0
			if tc.Index != nil {
				idx = *tc.Index
			}
			if _, ok := e.toolArgs[idx]; !ok {
				e.toolArgs[idx] = &strings.Builder{}
				e.toolOrder = append(e.toolOrder, idx)
			}
			if tc.Function.Name != "" {
				e.toolNames[idx] = tc.Function.Name
			}
			e.toolArgs[idx].WriteString(tc.Function.Arguments)
		}
		if ch.FinishReason != nil && *ch.FinishReason != "" {
			e.finish = *ch.FinishReason
		}
	}
	if c.Usage != nil {
		e.usage = c.Usage
	}
	return out
}

// flushToolCalls 收束的 functionCall → 独立帧(Gemini 以 STOP 收束工具调用)。
func (e *GeminiStreamEncoder) flushToolCalls() [][]byte {
	if len(e.toolOrder) == 0 {
		return nil
	}
	var parts []map[string]any
	for _, idx := range e.toolOrder {
		name := e.toolNames[idx]
		var args any = map[string]any{}
		if raw := e.toolArgs[idx].String(); raw != "" {
			if json.Unmarshal([]byte(raw), &args) != nil {
				args = map[string]any{}
			}
		}
		parts = append(parts, map[string]any{"functionCall": map[string]any{"name": name, "args": args}})
	}
	e.toolOrder = nil
	return [][]byte{e.geminiDeltaFrame(parts)}
}

// Complete 终帧(finishReason+usageMetadata;仅应调用一次)。
func (e *GeminiStreamEncoder) Complete() [][]byte {
	var out [][]byte
	out = append(out, e.flushToolCalls()...)
	u := map[string]int64{}
	if e.usage != nil {
		u = map[string]int64{"prompt": e.usage.PromptTokens, "completion": e.usage.CompletionTokens,
			"cached": e.usage.CachedTokens, "thoughts": e.usage.ReasoningTokens}
	}
	e.done = true
	return append(out, e.event(map[string]any{
		"candidates": []any{map[string]any{
			"content": map[string]any{"parts": []any{}, "role": "model"},
			"index":   0, "finishReason": geminiFinishReasonFromChat(e.finish),
		}},
		"usageMetadata": geminiUsage(u),
	}))
}

// Failed 流中失败:Gemini 错误信封帧(诚实失败,不伪造正常终帧)。
func (e *GeminiStreamEncoder) Failed(message string) []byte {
	return e.event(map[string]any{"error": map[string]any{
		"code": 500, "message": message, "status": "INTERNAL",
	}})
}
