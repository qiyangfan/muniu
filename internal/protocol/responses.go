// Responses API 请求转换为规范 chat 请求，响应和流事件再映射回 Responses 形态。
// 不支持的请求通过 ResponsesUnsupportedError 返回机器码。
package protocol

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Responses 翻译失败的机器码。
const (
	CodePreviousResponseIDUnsupported = "previous_response_id_unsupported"
	CodeBackgroundUnsupported         = "background_unsupported"
	CodeInputItemUnsupported          = "input_item_unsupported"
	CodeInputInvalid                  = "input_invalid"
)

// ResponsesUnsupportedError 表示请求包含不支持或无效的内容；handler 将其映射为 HTTP 400。
type ResponsesUnsupportedError struct{ Code string }

func (e *ResponsesUnsupportedError) Error() string { return "responses: " + e.Code }

// respPart 是 Responses 输入中的内容部件。
type respPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// respItem 是 Responses input 数组中的一项。
type respItem struct {
	Type string `json:"type"`
	Role string `json:"role"`
	// content 可以是字符串或部件数组；其他 JSON 类型无效。
	Content json.RawMessage `json:"content"`
	// function_call
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	// function_call_output
	Output string `json:"output"`
}

// contentParts 将字符串或部件数组统一为部件列表，其他 JSON 类型均拒绝。
func (it *respItem) contentParts() ([]respPart, error) {
	raw := it.Content
	switch {
	case len(raw) > 0 && raw[0] == '"':
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return nil, &ResponsesUnsupportedError{Code: CodeInputInvalid}
		}
		return []respPart{{Type: "input_text", Text: s}}, nil
	case len(raw) > 0 && raw[0] == '[':
		var parts []respPart
		if json.Unmarshal(raw, &parts) != nil {
			return nil, &ResponsesUnsupportedError{Code: CodeInputInvalid}
		}
		return parts, nil
	default:
		return nil, &ResponsesUnsupportedError{Code: CodeInputInvalid}
	}
}

// respTool 是 Responses API 的 function 工具定义。
type respTool struct {
	Type        string `json:"type"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters"`
}

type responsesRequest struct {
	Model              string          `json:"model"`
	Instructions       string          `json:"instructions"`
	Input              json.RawMessage `json:"input"`
	Tools              []respTool      `json:"tools"`
	ToolChoice         json.RawMessage `json:"tool_choice"`
	MaxOutputTokens    *int64          `json:"max_output_tokens"`
	Temperature        *float64        `json:"temperature"`
	TopP               *float64        `json:"top_p"`
	Stream             bool            `json:"stream"`
	PreviousResponseID *string         `json:"previous_response_id"`
	Background         *bool           `json:"background"`
}

// translateResponsesRequest 将 Responses 请求转换为规范 chat 请求，并提取 previous_response_id。
func translateResponsesRequest(body []byte) (*ChatRequest, string, error) {
	var rr responsesRequest
	if err := json.Unmarshal(body, &rr); err != nil {
		return nil, "", &ResponsesUnsupportedError{Code: CodeInputInvalid}
	}
	if rr.Background != nil && *rr.Background {
		return nil, "", &ResponsesUnsupportedError{Code: CodeBackgroundUnsupported}
	}
	if rr.Model == "" || len(rr.Input) == 0 {
		return nil, "", &ResponsesUnsupportedError{Code: CodeInputInvalid}
	}

	var b strings.Builder
	b.WriteString(`{"model":`)
	b.Write(mustJSON(rr.Model))
	if err := writeResponsesBody(&b, rr); err != nil {
		return nil, "", err
	}
	b.WriteString(`}`)

	// canonical JSON 经既有解析器回填 typed 字段，避免两套解析。
	req, err := ParseChatRequest([]byte(b.String()))
	if err != nil {
		return nil, "", err
	}
	// previous_response_id 仅提取透出，不参与翻译
	return req, pointerString(rr.PreviousResponseID), nil
}

// writeResponsesBody 追加 messages 及采样/工具相关字段，字段序即 wire 契约。
func writeResponsesBody(b *strings.Builder, rr responsesRequest) error {
	msgs, err := responsesMessagesToChat(rr)
	if err != nil {
		return err
	}
	b.WriteString(`,"messages":[` + strings.Join(msgs, ",") + `]`)
	writeResponsesSampling(b, &rr)
	tools, err := responsesToolsToChat(rr.Tools)
	if err != nil {
		return err
	}
	if tools != "" {
		b.WriteString(`,"tools":` + tools)
	}
	toolChoice, err := responsesToolChoiceToChat(rr.ToolChoice)
	if err != nil {
		return err
	}
	if toolChoice != "" {
		b.WriteString(`,"tool_choice":` + toolChoice)
	}
	return nil
}

// writeResponsesSampling 追加 stream 与采样参数。
func writeResponsesSampling(b *strings.Builder, rr *responsesRequest) {
	if rr.Stream {
		b.WriteString(`,"stream":true`)
	}
	if rr.MaxOutputTokens != nil {
		b.WriteString(`,"max_tokens":` + fmt.Sprint(*rr.MaxOutputTokens))
	}
	if rr.Temperature != nil {
		b.WriteString(`,"temperature":` + fmt.Sprint(*rr.Temperature))
	}
	if rr.TopP != nil {
		b.WriteString(`,"top_p":` + fmt.Sprint(*rr.TopP))
	}
}

// pointerString 解引用可空字符串，nil 为空串。
func pointerString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// responsesMessagesToChat instructions + input → chat 消息片段列表，元素为 JSON 字面量。
// input 支持字符串快捷形态与条目数组两形态，其他 JSON 类型拒绝。
func responsesMessagesToChat(rr responsesRequest) ([]string, error) {
	var msgs []string
	if rr.Instructions != "" {
		msgs = append(msgs, `{"role":"system","content":`+string(mustJSON(rr.Instructions))+`}`)
	}
	switch rr.Input[0] {
	case '"': // string 快捷形态 → 单条 user 消息
		msgs = append(msgs, `{"role":"user","content":`+string(rr.Input)+`}`)
		return msgs, nil
	case '[':
		var items []respItem
		if err := json.Unmarshal(rr.Input, &items); err != nil {
			return nil, &ResponsesUnsupportedError{Code: CodeInputInvalid}
		}
		for _, it := range items {
			out, err := respItemToChat(it)
			if err != nil {
				return nil, err
			}
			msgs = append(msgs, out)
		}
		return msgs, nil
	default:
		return nil, &ResponsesUnsupportedError{Code: CodeInputInvalid}
	}
}

// respItemToChat 单条 input 条目 → 一条 chat 消息字面量。
func respItemToChat(it respItem) (string, error) {
	typ := it.Type
	if typ == "" {
		// 官方缺省语义：携带 role 的条目即 message，官方 SDK 缺省产物；
		// 无 type 无 role 不做猜测，function_call 家族恒带 type。
		if it.Role == "" {
			return "", &ResponsesUnsupportedError{Code: CodeInputInvalid}
		}
		typ = "message"
	}
	switch typ {
	case "message":
		return respMessageToChat(it)
	case "function_call":
		args := it.Arguments
		if args == "" {
			args = "{}"
		}
		return `{"role":"assistant","content":null,"tool_calls":[` +
			chatToolCallJSON(it.CallID, it.Name, args) + `]}`, nil
	case "function_call_output":
		return chatToolMessageJSON(it.CallID, it.Output), nil
	default:
		return "", &ResponsesUnsupportedError{Code: CodeInputItemUnsupported}
	}
}

// respMessageToChat message 条目 → 单条纯文本 chat 消息，role 缺省为 user。
func respMessageToChat(it respItem) (string, error) {
	parts, err := it.contentParts()
	if err != nil {
		return "", err
	}
	text, err := partsToText(parts)
	if err != nil {
		return "", err
	}
	role := it.Role
	if role == "" {
		role = "user"
	}
	return chatUserTextJSON(role, text), nil
}

// responsesToolsToChat 工具定义 → chat function 数组字面量；无工具时返回空串。
func responsesToolsToChat(tools []respTool) (string, error) {
	if len(tools) == 0 {
		return "", nil
	}
	out := make([]map[string]any, 0, len(tools))
	for _, tl := range tools {
		if tl.Type != "function" {
			return "", &ResponsesUnsupportedError{Code: CodeInputItemUnsupported}
		}
		fn := map[string]any{"name": tl.Name}
		if tl.Description != "" {
			fn["description"] = tl.Description
		}
		if tl.Parameters != nil {
			fn["parameters"] = tl.Parameters
		}
		out = append(out, map[string]any{"type": "function", "function": fn})
	}
	tj, _ := json.Marshal(out)
	return string(tj), nil
}

// responsesToolChoiceToChat tool_choice → chat 形态字面量；缺省或 null 返回空串。
func responsesToolChoiceToChat(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var tc any
	if json.Unmarshal(raw, &tc) != nil {
		return "", &ResponsesUnsupportedError{Code: CodeInputInvalid}
	}
	if m, isM := tc.(map[string]any); isM {
		// 将 Responses 的具名工具选择转换为 Chat 的 function 对象。
		name, isS := m["name"].(string)
		switch {
		case isS:
			fn := map[string]any{"name": name}
			if desc, has := m["description"].(string); has {
				fn["description"] = desc
			}
			tc = map[string]any{"type": "function", "function": fn}
		case m["function"] == nil:
			return "", &ResponsesUnsupportedError{Code: CodeInputItemUnsupported}
		}
	}
	tj, _ := json.Marshal(tc)
	return string(tj), nil
}

// partsToText content parts → 文本，仅接受 input_text/output_text，其余类型显式拒绝。
func partsToText(parts []respPart) (string, error) {
	var sb strings.Builder
	for _, p := range parts {
		switch p.Type {
		case "input_text", "output_text", "text", "refusal":
			sb.WriteString(p.Text)
		default:
			return "", &ResponsesUnsupportedError{Code: CodeInputItemUnsupported}
		}
	}
	return sb.String(), nil
}

// mustJSON 字符串 → JSON 字符串字面量，含转义。
func mustJSON(s string) []byte {
	b, _ := json.Marshal(s)
	return b
}

// ---- 响应侧映射 ----

// responsesUsage chat usage → Responses usage，input/output/total 直取，cached/reasoning 入 details。
type responsesUsage struct {
	InputTokens         int64            `json:"input_tokens"`
	InputTokensDetails  map[string]int64 `json:"input_tokens_details,omitempty"`
	OutputTokens        int64            `json:"output_tokens"`
	OutputTokensDetails map[string]int64 `json:"output_tokens_details,omitempty"`
	TotalTokens         int64            `json:"total_tokens"`
}

// ResponsesObjectFromChat 非流式：chat.completion → Responses 对象。
// respID = "resp_" + request_id，与日志、usage_logs 的追责口径一致。
func ResponsesObjectFromChat(chatBody []byte, respID string) ([]byte, error) {
	var chat struct {
		Choices []struct {
			Message struct {
				Role             string  `json:"role"`
				Content          *string `json:"content"`
				ReasoningContent *string `json:"reasoning_content"`
				ToolCalls        []struct {
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Usage *Usage `json:"usage"`
	}
	if err := json.Unmarshal(chatBody, &chat); err != nil {
		return nil, err
	}
	output := []map[string]any{}
	for _, ch := range chat.Choices {
		if ch.Message.ReasoningContent != nil && *ch.Message.ReasoningContent != "" {
			output = append(output, map[string]any{"type": "reasoning", "text": *ch.Message.ReasoningContent})
		}
		if ch.Message.Content != nil {
			output = append(output, map[string]any{
				"type": "message", "role": "assistant", "status": "completed",
				"content": []map[string]any{{"type": "output_text", "text": *ch.Message.Content}},
			})
		}
		for _, tc := range ch.Message.ToolCalls {
			output = append(output, map[string]any{
				"type": "function_call", "call_id": tc.ID, "name": tc.Function.Name,
				"arguments": tc.Function.Arguments, "status": "completed",
			})
		}
	}
	resp := map[string]any{
		"id": respID, "object": "response", "status": "completed",
		"output": output,
	}
	if chat.Usage != nil {
		// reasoning_tokens 已包含在 completion_tokens 中，只写入明细以避免重复计数。
		u := responsesUsage{
			InputTokens:  chat.Usage.PromptTokens,
			OutputTokens: chat.Usage.CompletionTokens,
			TotalTokens:  chat.Usage.PromptTokens + chat.Usage.CompletionTokens,
		}
		if chat.Usage.CachedTokens > 0 {
			u.InputTokensDetails = map[string]int64{"cached_tokens": chat.Usage.CachedTokens}
		}
		if chat.Usage.ReasoningTokens > 0 {
			u.OutputTokensDetails = map[string]int64{"reasoning_tokens": chat.Usage.ReasoningTokens}
		}
		resp["usage"] = u
	}
	return json.Marshal(resp)
}

// ---- 流式事件编码器 ----

// toolItem 单个 function_call 工具项的流式累积态，按上游 tool_calls[].index 跟踪，
// 并行多工具各自独立 added/args/done，杜绝参数与 call_id 错配。
type toolItem struct {
	added  bool
	itemID string // 从 item_1 起，0 号留给文本；并行工具各自独立
	callID string
	name   string
	args   strings.Builder
}

// ResponsesStreamEncoder 把上游 OpenAI 形态 SSE 帧翻译为 Responses 最小事件集。
// 顺序不变式：created 首帧、completed 终帧、delta 只在两者之间。
// 文本单 item，function_call 按上游内层 index 多 item 独立配对；仅消费 index 0，其余忽略。
// 非并发安全，单请求单实例。
type ResponsesStreamEncoder struct {
	id, model string
	created   bool
	msgOpen   bool
	textOut   strings.Builder
	// reasoning 状态机(流式 reasoning_content 透传,openspec responses-stream-reasoning):
	// 事件词汇借官方 reasoning_summary_text 族,item 形态与非流式冻结契约同形(type=reasoning+text 原文)。
	reasonOpen bool
	reasonBuf  strings.Builder
	tools      map[int]*toolItem
	toolOrder  []int
	usage      *Usage
	done       bool
}

// NewResponsesStreamEncoder 构造，id 应传 resp_<request_id>。
func NewResponsesStreamEncoder(id, model string) *ResponsesStreamEncoder {
	return &ResponsesStreamEncoder{id: id, model: model, tools: map[int]*toolItem{}}
}

func (e *ResponsesStreamEncoder) event(v map[string]any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// Init 首帧 response.created，在任一 delta 之前恰好调用一次。
func (e *ResponsesStreamEncoder) Init() []byte {
	e.created = true
	return e.event(map[string]any{"type": "response.created", "response": map[string]any{
		"id": e.id, "object": "response", "status": "in_progress", "model": e.model, "output": []any{},
	}})
}

func (e *ResponsesStreamEncoder) ensureCreated(out *[][]byte) {
	if !e.created {
		*out = append(*out, e.Init())
	}
}

// closeReasoning 收束推理 item：summary done（累计原文）+ output_item.done（形态与
// 非流式冻结契约同形：type=reasoning + text 原文）。reasonOpen 时生效，否则 no-op。
func (e *ResponsesStreamEncoder) closeReasoning(out *[][]byte) {
	if !e.reasonOpen {
		return
	}
	e.reasonOpen = false
	*out = append(*out, e.event(map[string]any{
		"type": "response.reasoning_summary_text.done", "item_id": "item_reasoning",
		"output_index": 0, "text": e.reasonBuf.String(),
	}))
	*out = append(*out, e.event(map[string]any{
		"type": "response.output_item.done", "output_index": 0,
		"item": map[string]any{"type": "reasoning", "text": e.reasonBuf.String(), "status": "completed"},
	}))
	e.reasonBuf.Reset()
}

// closeMessage 收束文本 item，msgOpen 时发 done 并复位，否则 no-op。
func (e *ResponsesStreamEncoder) closeMessage(out *[][]byte) {
	if !e.msgOpen {
		return
	}
	e.msgOpen = false
	*out = append(*out, e.event(map[string]any{
		"type": "response.output_item.done", "output_index": 0,
		"item": map[string]any{"type": "message", "role": "assistant", "status": "completed",
			"content": []map[string]any{{"type": "output_text", "text": e.textOut.String()}}},
	}))
	e.textOut.Reset()
}

// Feed 消费一帧上游 OpenAI chunk(delta/usage/finish)，返回待写的 Responses 事件序列。
// usage 恒捕获，兼容 finish 帧与独立 usage 帧；reasoning_content 经官方词汇透传
// (openspec responses-stream-reasoning，item 形态与非流式同形)。
func (e *ResponsesStreamEncoder) Feed(chunk []byte) [][]byte {
	c, err := ParseStreamChunk(chunk)
	if err != nil {
		return nil
	}
	if e.done {
		if c.Usage != nil {
			e.usage = c.Usage
		}
		return nil
	}
	var out [][]byte
	e.ensureCreated(&out)
	for _, ch := range c.Choices {
		if ch.Index != nil && *ch.Index != 0 {
			continue // v1 仅映射首 choice，多 choice 映射未定义，防御合流错乱
		}
		e.feedChoice(&out, &ch)
	}
	if c.Usage != nil {
		e.usage = c.Usage
	}
	return out
}

// feedChoice 单 choice 的增量映射：推理、正文、工具调用按固定顺序处理，最后吸收 finish_reason。
func (e *ResponsesStreamEncoder) feedChoice(out *[][]byte, ch *StreamChoice) {
	e.feedReasoning(out, ch.Delta.ReasoningContent)
	e.feedText(out, ch.Delta.Content)
	e.feedToolCalls(out, ch.Delta.ToolCalls)
	if ch.FinishReason != nil && *ch.FinishReason != "" && !e.done {
		e.finish(out)
	}
}

// feedToolCalls 累积单 choice 的全部工具调用增量。
func (e *ResponsesStreamEncoder) feedToolCalls(out *[][]byte, raws []json.RawMessage) {
	for _, raw := range raws {
		e.feedToolCall(out, raw)
	}
}

// feedReasoning 推理增量：首个增量先对称收束 message，再发 output_item.added；
// 随后累计原文并发射 summary_text.delta。
func (e *ResponsesStreamEncoder) feedReasoning(out *[][]byte, text *string) {
	if text == nil || *text == "" {
		return
	}
	if !e.reasonOpen {
		e.closeMessage(out) // 混流对称收束：正文→推理切换先收 message
		*out = append(*out, e.event(map[string]any{"type": "response.output_item.added",
			"item_id": "item_reasoning", "output_index": 0,
			"item": map[string]any{"type": "reasoning", "status": "in_progress"}}))
		e.reasonOpen = true
	}
	e.reasonBuf.WriteString(*text)
	*out = append(*out, e.event(map[string]any{
		"type": "response.reasoning_summary_text.delta", "item_id": "item_reasoning",
		"output_index": 0, "delta": *text,
	}))
}

// feedText 正文增量：切换时先收束推理 item，首个增量发 message 的 output_item.added。
func (e *ResponsesStreamEncoder) feedText(out *[][]byte, text *string) {
	if text == nil || *text == "" {
		return
	}
	if e.reasonOpen {
		e.closeReasoning(out) // 混流对称收束：推理→正文切换先收 reasoning
	}
	if !e.msgOpen {
		*out = append(*out, e.event(map[string]any{"type": "response.output_item.added",
			"output_index": 0, "item": map[string]any{"type": "message", "role": "assistant",
				"status": "in_progress", "content": []any{}}}))
		e.msgOpen = true
	}
	e.textOut.WriteString(*text)
	*out = append(*out, e.event(map[string]any{
		"type": "response.output_text.delta", "item_id": "item_0", "output_index": 0, "content_index": 0,
		"delta": *text,
	}))
}

// feedToolCall 单个 tool_calls 增量：按内层 index 累积，首次出现从 item_1 起独立编号。
func (e *ResponsesStreamEncoder) feedToolCall(out *[][]byte, raw json.RawMessage) {
	var tc struct {
		Index    *int   `json:"index"`
		ID       string `json:"id"`
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
	ti, ok := e.tools[idx]
	if !ok {
		ti = &toolItem{itemID: fmt.Sprintf("item_%d", len(e.toolOrder)+1)}
		e.tools[idx] = ti
		e.toolOrder = append(e.toolOrder, idx)
	}
	if tc.ID != "" {
		ti.callID = tc.ID
	}
	if tc.Function.Name != "" {
		ti.name = tc.Function.Name
	}
	if !ti.added {
		// 首个工具 item 开启前对称收束文本/推理 item(混流不交叉,MUST):
		// reasoning→tools 与 content→tools 两剧本均收束后再 added。
		e.closeReasoning(out)
		e.closeMessage(out)
		// 先创建 item，允许后续帧补齐 call_id 和 name。
		ti.added = true
		*out = append(*out, e.event(map[string]any{"type": "response.output_item.added",
			"item_id": ti.itemID, "output_index": 0, "item": map[string]any{"type": "function_call",
				"call_id": ti.callID, "name": ti.name, "arguments": "", "status": "in_progress"}}))
	}
	if tc.Function.Arguments != "" && ti.added {
		ti.args.WriteString(tc.Function.Arguments)
		*out = append(*out, e.event(map[string]any{
			"type": "response.function_call_arguments.delta", "item_id": ti.itemID, "output_index": 0,
			"delta": tc.Function.Arguments,
		}))
	}
}

// finish 上游 finish_reason 收尾：对称收束推理/文本，并对已开启的工具 item 逐个发 done。
func (e *ResponsesStreamEncoder) finish(out *[][]byte) {
	e.done = true
	e.closeReasoning(out)
	e.closeMessage(out)
	for _, idx := range e.toolOrder {
		if ti := e.tools[idx]; ti != nil && ti.added {
			*out = append(*out, e.event(map[string]any{
				"type": "response.output_item.done", "output_index": 0,
				"item": map[string]any{"type": "function_call", "call_id": ti.callID,
					"name": ti.name, "arguments": ti.args.String(), "status": "completed"}}))
		}
	}
}

// Complete 终帧 response.completed，usage 恒携带，无上游 usage 时补零值，仅应调用一次。
func (e *ResponsesStreamEncoder) Complete() []byte {
	e.created = true
	u := responsesUsage{}
	if e.usage != nil {
		// reasoning_tokens 已包含在 completion_tokens 中，只写入明细以避免重复计数。
		u = responsesUsage{
			InputTokens:  e.usage.PromptTokens,
			OutputTokens: e.usage.CompletionTokens,
			TotalTokens:  e.usage.PromptTokens + e.usage.CompletionTokens,
		}
		if e.usage.CachedTokens > 0 {
			u.InputTokensDetails = map[string]int64{"cached_tokens": e.usage.CachedTokens}
		}
		if e.usage.ReasoningTokens > 0 {
			u.OutputTokensDetails = map[string]int64{"reasoning_tokens": e.usage.ReasoningTokens}
		}
	}
	resp := map[string]any{
		"id": e.id, "object": "response", "status": "completed", "model": e.model, "usage": u,
	}
	return e.event(map[string]any{"type": "response.completed", "response": resp})
}

// Failed 流中失败：response.failed，不伪造 completed。
func (e *ResponsesStreamEncoder) Failed(message string) []byte {
	return e.event(map[string]any{"type": "response.failed", "response": map[string]any{
		"id": e.id, "object": "response", "status": "failed", "error": map[string]any{"message": message},
	}})
}

// ChatFromResponsesObject 将 Responses 对象逆映射为 canonical chat 响应，供直通双存，
// 非对外翻译面。支持 message/reasoning/function_call 与 usage；其余 item 宁缺勿错。
// finish_reason 按 status 保真：completed→stop、incomplete→length；伪造 stop 会让未完成
// 响应经缓存准入流入跨协议回放。
func ChatFromResponsesObject(body []byte) ([]byte, error) {
	var resp struct {
		Output []respOutputItem `json:"output"`
		Usage  *responsesUsage  `json:"usage"`
		Status string           `json:"status"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	finish := "stop"
	if resp.Status == "incomplete" {
		finish = "length"
	}
	var acc responsesObjectAcc
	for _, it := range resp.Output {
		acc.add(it)
	}
	out := map[string]any{
		"object":  "chat.completion",
		"choices": []any{map[string]any{"index": 0, "message": acc.message(), "finish_reason": finish}},
	}
	if resp.Usage != nil {
		if u := chatUsageFromResponses(resp.Usage); u != (Usage{}) {
			out["usage"] = u
		}
	}
	return json.Marshal(out)
}

// responsesObjectAcc 逆映射累积态：message 正文拼接、reasoning 按序拼接、function_call 依序编号。
type responsesObjectAcc struct {
	content   string
	reasoning strings.Builder
	calls     []chatToolCall
}

// add 按 item 类型累积；其余类型宁缺勿错，直接忽略。
func (a *responsesObjectAcc) add(it respOutputItem) {
	switch it.Type {
	case "message":
		for _, c := range it.Content {
			if c.Type == "output_text" {
				a.content += c.Text
			}
		}
	case "reasoning":
		a.reasoning.WriteString(it.Text)
		for _, sm := range it.Summary {
			a.reasoning.WriteString(sm.Text)
		}
	case "function_call":
		a.calls = append(a.calls, newChatToolCall(len(a.calls), it.CallID, it.Name, it.Arguments))
	}
}

// message chat assistant 消息；reasoning/tool_calls 非空才出现，保持字段缺省形态。
func (a *responsesObjectAcc) message() map[string]any {
	msg := map[string]any{"role": "assistant", "content": a.content}
	if a.reasoning.Len() > 0 {
		msg["reasoning_content"] = a.reasoning.String()
	}
	if len(a.calls) > 0 {
		msg["tool_calls"] = a.calls
	}
	return msg
}

// respOutputItem 是 ChatFromResponsesObject 读取的 output 条目子集。
type respOutputItem struct {
	Type string `json:"type"`
	Text string `json:"text"`
	// reasoning 摘要：官方形态为 summary 数组，元素为 summary_text 部件，顶层 text 兼容
	// 简化实现。两处按序拼接，皆空即 canonical 无 reasoning。
	Summary []struct {
		Text string `json:"text"`
	} `json:"summary"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// chatToolCall 是 chat 响应内 tool_calls 元素的固定形态（非流式完整对象）。
type chatToolCall struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

func newChatToolCall(index int, id, name, arguments string) chatToolCall {
	tc := chatToolCall{Index: index, ID: id, Type: "function"}
	tc.Function.Name = name
	tc.Function.Arguments = arguments
	return tc
}

// chatUsageFromResponses Responses usage → canonical usage，details 子键取 cached/reasoning。
func chatUsageFromResponses(src *responsesUsage) Usage {
	u := Usage{
		PromptTokens:     src.InputTokens,
		CompletionTokens: src.OutputTokens,
	}
	if d := src.InputTokensDetails; d != nil {
		u.CachedTokens = d["cached_tokens"]
	}
	if d := src.OutputTokensDetails; d != nil {
		u.ReasoningTokens = d["reasoning_tokens"]
	}
	return u
}
