// Gemini generateContent 请求转换为规范 chat 请求，响应和流事件再转换为 Gemini 形态。
// 不支持的请求通过 GeminiUnsupportedError 返回机器码。
package protocol

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Gemini 翻译失败的机器码。
const (
	CodeGeminiContentPartUnsupported  = "content_part_unsupported"
	CodeGeminiStreamFormUnsupported   = "stream_form_unsupported"
	CodeGeminiInputInvalid            = "input_invalid"
	CodeGeminiModelNotFound           = "model_not_found"
	CodeGeminiToolUnsupported         = "tool_unsupported"
	CodeGeminiPreviousIDNotApplicable = "previous_id_not_applicable"
)

// GeminiUnsupportedError 表示请求包含不支持或无效的内容。
type GeminiUnsupportedError struct {
	Code   string // 机器码，muniu 追责口径
	Status string // Gemini status 字面量，如 INVALID_ARGUMENT，由 SDK 消费
}

func (e *GeminiUnsupportedError) Error() string { return "gemini: " + e.Code }

func geminiInvalid() *GeminiUnsupportedError {
	return &GeminiUnsupportedError{Code: CodeGeminiInputInvalid, Status: "INVALID_ARGUMENT"}
}

// ---- 入站请求侧 DTO ----

// geminiPartFunctionCall / geminiPartFunctionResponse 独立命名以复用部件翻译辅助函数。
type geminiPartFunctionCall struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

type geminiPartFunctionResponse struct {
	Name     string          `json:"name"`
	Response json.RawMessage `json:"response"`
}

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
	FunctionCall     *geminiPartFunctionCall     `json:"functionCall"`
	FunctionResponse *geminiPartFunctionResponse `json:"functionResponse"`
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
	TopK            *float64 `json:"topK"`
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

// TranslateGeminiRequest 将 generateContent 请求转换为规范 chat 请求；模型名来自路径，流式模式由端点决定。
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

	msgs, err := geminiMessagesToChat(gr)
	if err != nil {
		return nil, err
	}
	// 空串表示不写 tool_choice（无 toolConfig、或 mode 未声明不映射）
	toolChoice, err := geminiToolChoiceToChat(gr.ToolConfig)
	if err != nil {
		return nil, err
	}
	tools, err := geminiToolsToChat(gr.Tools)
	if err != nil {
		return nil, err
	}

	var b strings.Builder
	b.WriteString(`{"model":`)
	b.Write(mustJSON(pathModel))
	b.WriteString(`,"messages":[` + strings.Join(msgs, ",") + `]`)
	if stream {
		b.WriteString(`,"stream":true`)
	}
	writeGeminiSampling(&b, &gr.GenerationConfig)
	if tools != "" {
		b.WriteString(`,"tools":` + tools)
	}
	if toolChoice != "" {
		b.WriteString(`,"tool_choice":` + toolChoice)
	}
	b.WriteString(`}`)

	// canonical JSON 经既有解析器回填 typed 字段，避免两套解析。
	return ParseChatRequest([]byte(b.String()))
}

// writeGeminiSampling generationConfig → chat 采样参数，字段序即 wire 契约。
func writeGeminiSampling(b *strings.Builder, gc *geminiGenConfig) {
	if gc.Temperature != nil {
		b.WriteString(`,"temperature":` + fmt.Sprint(*gc.Temperature))
	}
	if gc.TopP != nil {
		b.WriteString(`,"top_p":` + fmt.Sprint(*gc.TopP))
	}
	// topK → canonical top_k，qwen 系字段；与 top_p 同路透传，治理 Raw 层可裁决
	if gc.TopK != nil {
		b.WriteString(`,"top_k":` + fmt.Sprint(*gc.TopK))
	}
	if gc.MaxOutputTokens != nil && *gc.MaxOutputTokens > 0 {
		b.WriteString(`,"max_tokens":` + fmt.Sprint(*gc.MaxOutputTokens))
	}
	if len(gc.StopSequences) > 0 {
		tj, _ := json.Marshal(gc.StopSequences)
		b.WriteString(`,"stop":` + string(tj))
	}
}

// geminiToolChoiceToChat toolConfig → chat tool_choice 字面量；缺省或未声明 mode 时返回空串。
func geminiToolChoiceToChat(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	return translateGeminiToolConfig(raw)
}

// geminiMessagesToChat 展开为「system 消息 + 逐 content 翻译」的 chat 消息片段列表，元素为 JSON 字面量。
func geminiMessagesToChat(gr geminiRequest) ([]string, error) {
	var msgs []string
	// systemInstruction：字符串快捷形态或 {parts:[...]} 块形态 → 单条 system 消息
	if len(gr.SystemInstruction) > 0 && string(gr.SystemInstruction) != "null" {
		sys, err := geminiSystemToText(gr.SystemInstruction)
		if err != nil {
			return nil, err
		}
		if sys != "" {
			msgs = append(msgs, `{"role":"system","content":`+string(mustJSON(sys))+`}`)
		}
	}
	// functionCall 合成 id：同名调用按序去重，并行同名调用的 id 配对为已知限制
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
	return msgs, nil
}

// geminiToolsToChat 工具声明 → chat function 数组字面量；无有效声明时返回空串。
func geminiToolsToChat(tools []geminiTool) (string, error) {
	if len(tools) == 0 {
		return "", nil
	}
	out := make([]map[string]any, 0, len(tools))
	for _, tl := range tools {
		for _, fd := range tl.FunctionDeclarations {
			fn := map[string]any{"name": fd.Name}
			if fd.Description != "" {
				fn["description"] = fd.Description
			}
			if len(fd.Parameters) > 0 && string(fd.Parameters) != "null" {
				var schema any
				if json.Unmarshal(fd.Parameters, &schema) != nil {
					return "", geminiInvalid()
				}
				fn["parameters"] = schema
			}
			out = append(out, map[string]any{"type": "function", "function": fn})
		}
	}
	if len(out) == 0 {
		return "", nil
	}
	tj, _ := json.Marshal(out)
	return string(tj), nil
}

// geminiSystemToText systemInstruction → 文本，接受字符串快捷形态或 text 部件块。
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

// geminiPartsToChat 单 content 的 parts → 0..n 条 chat 消息：
//   - 纯 text 与 inlineData image → 同角色消息，多模态经 image_url data URL 承载；
//   - assistant 的 functionCall → assistant(tool_calls) 消息，id 按名合成、同名按序去重；
//   - user 的 functionResponse → role:tool 消息，tool_call_id 回指同名最近的 functionCall。
func geminiPartsToChat(
	role string, parts []geminiPart, callSeq map[string]int, lastCallID map[string]string,
) ([]string, error) {
	acc := &geminiPartsAcc{role: role}
	for _, p := range parts {
		// 按部件字段顺序处理：text 与 inlineData 可并存于同一部件，函数部件互斥
		if p.Text != "" {
			acc.textParts = append(acc.textParts, p.Text)
		}
		if p.InlineData != nil {
			if !strings.HasPrefix(p.InlineData.MimeType, "image/") {
				return nil, &GeminiUnsupportedError{Code: CodeGeminiContentPartUnsupported, Status: "INVALID_ARGUMENT"}
			}
			acc.imageURLs = append(acc.imageURLs, "data:"+p.InlineData.MimeType+";base64,"+p.InlineData.Data)
		}
		if p.FileData != nil {
			return nil, &GeminiUnsupportedError{Code: CodeGeminiContentPartUnsupported, Status: "INVALID_ARGUMENT"}
		}
		if p.FunctionCall != nil {
			acc.flushText()
			acc.toolCalls = append(acc.toolCalls, geminiFunctionCallToChat(p.FunctionCall, callSeq, lastCallID))
		}
		if p.FunctionResponse != nil {
			acc.flushText()
			out, err := geminiFunctionResponseToChat(p.FunctionResponse, lastCallID)
			if err != nil {
				return nil, err
			}
			acc.msgs = append(acc.msgs, out)
		}
		// thought 请求部件丢弃，签名回传语义不成立
	}
	return acc.done(), nil
}

// geminiPartsAcc 单 content 的 parts 翻译累积态：文本与图片等待合并，工具调用成组输出。
type geminiPartsAcc struct {
	role      string
	textParts []string
	imageURLs []string
	toolCalls []string
	msgs      []string
}

func (a *geminiPartsAcc) flushText() {
	if len(a.textParts) == 0 && len(a.imageURLs) == 0 {
		return
	}
	if len(a.imageURLs) == 0 {
		a.msgs = append(a.msgs, chatUserTextJSON(a.role, strings.Join(a.textParts, "\n")))
	} else {
		// 多模态：canonical image_url 形态，data URL 由 inlineData 合成
		content := `[`
		for i, u := range a.imageURLs {
			if i > 0 {
				content += `,`
			}
			content += `{"type":"image_url","image_url":{"url":` + string(mustJSON(u)) + `}}`
		}
		if len(a.textParts) > 0 {
			content += `,{"type":"text","text":` + string(mustJSON(strings.Join(a.textParts, "\n"))) + `}`
		}
		content += `]`
		a.msgs = append(a.msgs, `{"role":`+string(mustJSON(a.role))+`,"content":`+content+`}`)
	}
	a.textParts, a.imageURLs = nil, nil
}

// done 收尾：有工具调用时文本并入 tool_calls 消息(content 为 null 或合并文本)，否则补发纯文本消息。
func (a *geminiPartsAcc) done() []string {
	if len(a.toolCalls) > 0 {
		content := "null"
		if len(a.textParts) > 0 {
			content = string(mustJSON(strings.Join(a.textParts, "\n")))
		}
		a.msgs = append(a.msgs, `{"role":`+string(mustJSON(a.role))+`,"content":`+content+
			`,"tool_calls":[`+strings.Join(a.toolCalls, ",")+`]}`)
		return a.msgs
	}
	a.flushText()
	return a.msgs
}

// geminiFunctionCallToChat functionCall → chat tool_calls 元素字面量，并按名登记 id 供 response 回指。
func geminiFunctionCallToChat(fc *geminiPartFunctionCall, callSeq map[string]int, lastCallID map[string]string) string {
	args := "{}"
	if len(fc.Args) > 0 && string(fc.Args) != "null" {
		args = string(fc.Args)
	}
	name := fc.Name
	callSeq[name]++
	id := "call_" + name
	if callSeq[name] > 1 {
		id = fmt.Sprintf("%s_%d", id, callSeq[name])
	}
	lastCallID[name] = id
	return chatToolCallJSON(id, name, args)
}

// geminiFunctionResponseToChat functionResponse → role:tool 消息字面量；response 先于 call 属非法序列。
func geminiFunctionResponseToChat(fr *geminiPartFunctionResponse, lastCallID map[string]string) (string, error) {
	id, ok := lastCallID[fr.Name]
	if !ok {
		return "", geminiInvalid()
	}
	content := "{}"
	if len(fr.Response) > 0 && string(fr.Response) != "null" {
		content = string(fr.Response)
	}
	return chatToolMessageJSON(id, content), nil
}

// translateGeminiToolConfig functionCallingConfig.mode → chat tool_choice，完整映射含 NONE。
// allowedFunctionNames 单元素 → 具名形态；多元素或无 mode 无名单 → 静默丢弃。
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
			name := string(mustJSON(cfg.FunctionCallingConfig.AllowedFunctionNames[0]))
			return `{"type":"function","function":{"name":` + name + `}}`, nil
		}
		return `"required"`, nil
	case "NONE":
		return `"none"`, nil
	case "":
		return "", nil // 未声明 mode，不映射，保持上游缺省行为
	default:
		return "", geminiInvalid()
	}
}

// ---- 响应侧映射：canonical chat → Gemini 形态 ----

// geminiFinishReasonFromChat chat finish_reason → Gemini finishReason 的固定映射，
// 未知值兜底 STOP，与出站适配器映射互为镜像。
func geminiFinishReasonFromChat(finish string) string {
	switch finish {
	case "length":
		return "MAX_TOKENS"
	case "content_filter":
		return "SAFETY"
	default: // stop，含 tool_calls，Gemini 以 STOP 收束工具调用
		return "STOP"
	}
}

// GenerateContentResponseFromChat 非流式：chat.completion → GenerateContentResponse。
// reasoning_content → thought 部件，仅展示用，签名回传语义不成立；
// tool_calls → functionCall 部件，args 反序列化为对象形态。
func GenerateContentResponseFromChat(chatBody []byte, modelVersion string) ([]byte, error) {
	var chat struct {
		Choices []struct {
			Message      geminiChoiceMessage `json:"message"`
			FinishReason string              `json:"finish_reason"`
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
		parts = chatMessageToGeminiParts(ch.Message)
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

// geminiChoiceMessage 是 GenerateContentResponseFromChat 读取的上游 choice.message 子集。
type geminiChoiceMessage struct {
	Content          *string `json:"content"`
	ReasoningContent *string `json:"reasoning_content"`
	ToolCalls        []struct {
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	} `json:"tool_calls"`
}

// chatMessageToGeminiParts chat 消息 → Gemini parts：thought、text 与 functionCall 按此序发射；
// arguments 非对象 JSON 或空串时 args 退化为空对象。
func chatMessageToGeminiParts(msg geminiChoiceMessage) []map[string]any {
	var parts []map[string]any
	if msg.ReasoningContent != nil && *msg.ReasoningContent != "" {
		parts = append(parts, map[string]any{"text": *msg.ReasoningContent, "thought": true})
	}
	if msg.Content != nil && *msg.Content != "" {
		parts = append(parts, map[string]any{"text": *msg.Content})
	}
	for _, tc := range msg.ToolCalls {
		var args any
		if json.Unmarshal([]byte(tc.Function.Arguments), &args) != nil || tc.Function.Arguments == "" {
			args = map[string]any{}
		}
		parts = append(parts, map[string]any{"functionCall": map[string]any{"name": tc.Function.Name, "args": args}})
	}
	return parts
}

// geminiUsage 统一口径 → usageMetadata，total=prompt+completion，cached/thoughts 非零才带。
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

// ---- 流式事件编码器：上游 OpenAI chunk → alt=sse Gemini 帧 ----

// GeminiStreamEncoder 把上游 OpenAI 形态 SSE 帧翻译为 Gemini 流帧。顺序不变式：
// 内容增量帧在前，携带 finishReason+usageMetadata 的终帧收尾；工具调用在收束帧一次性发出，
// Gemini functionCall 部件为完整对象，无增量形态。非并发安全，单请求单实例。
type GeminiStreamEncoder struct {
	modelVersion string
	usage        *Usage
	finish       string
	toolArgs     map[int]*strings.Builder
	toolNames    map[int]string
	toolOrder    []int
	done         bool
}

// NewGeminiStreamEncoder 构造；modelVersion 回显给客户端，展示用。
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

func (e *GeminiStreamEncoder) geminiDeltaFrame(parts []map[string]any) []byte {
	return e.event(map[string]any{"candidates": []any{map[string]any{
		"content": map[string]any{"parts": parts, "role": "model"}, "index": 0,
	}}})
}

// Feed 消费一帧上游 OpenAI chunk，返回待写的 Gemini 流帧，可空。
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
			continue // 仅映射首 choice，防御多 choice 合流错乱
		}
		if parts := geminiDeltaParts(&ch.Delta); len(parts) > 0 {
			out = append(out, e.geminiDeltaFrame(parts))
		}
		e.accumulateToolCalls(ch.Delta.ToolCalls)
		if ch.FinishReason != nil && *ch.FinishReason != "" {
			e.finish = *ch.FinishReason
		}
	}
	if c.Usage != nil {
		e.usage = c.Usage
	}
	return out
}

// accumulateToolCalls 累积单 choice 的全部工具调用增量。
func (e *GeminiStreamEncoder) accumulateToolCalls(raws []json.RawMessage) {
	for _, raw := range raws {
		e.accumulateToolCall(raw)
	}
}

// geminiDeltaParts 单 choice 增量 → Gemini 部件：reasoning 在前为 thought，正文在后。
func geminiDeltaParts(d *Delta) []map[string]any {
	var parts []map[string]any
	if d.ReasoningContent != nil && *d.ReasoningContent != "" {
		parts = append(parts, map[string]any{"text": *d.ReasoningContent, "thought": true})
	}
	if d.Content != nil && *d.Content != "" {
		parts = append(parts, map[string]any{"text": *d.Content})
	}
	return parts
}

// accumulateToolCall 按内层 index 累积工具名与参数串；Gemini 无增量形态，收束时才成帧。
func (e *GeminiStreamEncoder) accumulateToolCall(raw json.RawMessage) {
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
	_, _ = e.toolArgs[idx].WriteString(tc.Function.Arguments)
}

// flushToolCalls 收束的 functionCall → 独立帧，Gemini 以 STOP 收束工具调用。
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

// Complete 终帧，携带 finishReason+usageMetadata，仅应调用一次。
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

// Failed 流中失败：Gemini 错误信封帧，不伪造正常终帧。
func (e *GeminiStreamEncoder) Failed(message string) []byte {
	return e.event(map[string]any{"error": map[string]any{
		"code": 500, "message": message, "status": "INTERNAL",
	}})
}
