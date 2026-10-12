// Anthropic Messages 入站翻译(ADR-0013;契约=proxy-and-streaming.md「Anthropic 入站翻译」):
// POST /v1/messages 边缘翻译为规范 chat 请求后复用既有管道;响应/流式事件反向映射为
// Anthropic message 对象与 SSE 事件序列。翻译失败以 MessagesUnsupportedError 携带机器码,
// handler 映射 400;全程零 float 运算(仅透传客户端数值)。
package protocol

import (
	"encoding/json"
	"fmt"
	"strings"
)

// 显式拒绝机器码(handler 映射 400;禁散落硬编码)。
const (
	CodeContentBlockUnsupported = "content_block_unsupported"
)

// MessagesUnsupportedError 显式拒绝(不支持的内容块/非法请求);handler 映射 400 + code。
type MessagesUnsupportedError struct{ Code string }

func (e *MessagesUnsupportedError) Error() string { return "messages: " + e.Code }

// msgBlock Anthropic content block:仅 text/tool_use/tool_result 可翻译;
// thinking 请求块丢弃(v1 已登记限制:签名回传语义不成立);其余显式拒绝。
type msgBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
	// tool_use
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
	// tool_result
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
}

type msgMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// msgTool Anthropic 顶层工具(input_schema 在 chat 侧更名 parameters)。
type msgTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type messagesRequest struct {
	Model         string          `json:"model"`
	MaxTokens     *int64          `json:"max_tokens"`
	System        json.RawMessage `json:"system"`
	Messages      []msgMessage    `json:"messages"`
	Temperature   *float64        `json:"temperature"`
	TopP          *float64        `json:"top_p"`
	StopSequences []string        `json:"stop_sequences"`
	Stream        bool            `json:"stream"`
	Tools         []msgTool       `json:"tools"`
	ToolChoice    json.RawMessage `json:"tool_choice"`
}

// translateMessagesRequest Anthropic Messages 请求 → 规范 chat 请求(映射表见设计文档)。
// 返回的 ChatRequest.Raw 即 canonical chat JSON 字节——治理/缓存/计量与等价 chat 请求同构。
func translateMessagesRequest(body []byte) (*ChatRequest, error) {
	var mr messagesRequest
	if err := json.Unmarshal(body, &mr); err != nil {
		return nil, &MessagesUnsupportedError{Code: CodeInputInvalid}
	}
	if mr.Model == "" || len(mr.Messages) == 0 {
		return nil, &MessagesUnsupportedError{Code: CodeInputInvalid}
	}

	var b strings.Builder
	b.WriteString(`{"model":`)
	b.Write(mustJSON(mr.Model))

	var msgs []string
	// system:字符串或 text 块数组 → 单条 system 消息(多块以换行合并)
	if len(mr.System) > 0 && string(mr.System) != "null" {
		sys, err := systemToText(mr.System)
		if err != nil {
			return nil, err
		}
		if sys != "" {
			msgs = append(msgs, `{"role":"system","content":`+string(mustJSON(sys))+`}`)
		}
	}
	for i := range mr.Messages {
		out, err := messageToChat(&mr.Messages[i])
		if err != nil {
			return nil, err
		}
		msgs = append(msgs, out...)
	}
	b.WriteString(`,"messages":[` + strings.Join(msgs, ",") + `]`)
	if mr.Stream {
		b.WriteString(`,"stream":true`)
	}
	if mr.MaxTokens != nil && *mr.MaxTokens > 0 {
		b.WriteString(`,"max_tokens":` + fmt.Sprint(*mr.MaxTokens))
	}
	if mr.Temperature != nil {
		b.WriteString(`,"temperature":` + fmt.Sprint(*mr.Temperature))
	}
	if mr.TopP != nil {
		b.WriteString(`,"top_p":` + fmt.Sprint(*mr.TopP))
	}
	if len(mr.StopSequences) > 0 {
		tj, _ := json.Marshal(mr.StopSequences)
		b.WriteString(`,"stop":` + string(tj))
	}
	if len(mr.Tools) > 0 {
		out := make([]map[string]any, 0, len(mr.Tools))
		for _, tl := range mr.Tools {
			fn := map[string]any{"name": tl.Name}
			if tl.Description != "" {
				fn["description"] = tl.Description
			}
			if len(tl.InputSchema) > 0 {
				var schema any
				if json.Unmarshal(tl.InputSchema, &schema) != nil {
					return nil, &MessagesUnsupportedError{Code: CodeInputInvalid}
				}
				fn["parameters"] = schema
			}
			out = append(out, map[string]any{"type": "function", "function": fn})
		}
		tj, _ := json.Marshal(out)
		b.WriteString(`,"tools":` + string(tj))
	}
	if len(mr.ToolChoice) > 0 && string(mr.ToolChoice) != "null" {
		tc, err := translateToolChoice(mr.ToolChoice)
		if err != nil {
			return nil, err
		}
		b.WriteString(`,"tool_choice":` + tc)
	}
	b.WriteString(`}`)

	// canonical JSON 经既有解析器回填 typed 字段(单源,避免两套解析)
	return ParseChatRequest([]byte(b.String()))
}

// systemToText system 字段 → 文本(字符串快捷形态或 text 块数组;其余拒绝)。
func systemToText(raw json.RawMessage) (string, error) {
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return "", &MessagesUnsupportedError{Code: CodeInputInvalid}
		}
		return s, nil
	}
	var blocks []msgBlock
	if json.Unmarshal(raw, &blocks) != nil {
		return "", &MessagesUnsupportedError{Code: CodeInputInvalid}
	}
	var parts []string
	for _, bl := range blocks {
		if bl.Type != "text" {
			return "", &MessagesUnsupportedError{Code: CodeContentBlockUnsupported}
		}
		parts = append(parts, bl.Text)
	}
	return strings.Join(parts, "\n"), nil
}

// messageToChat 单条 Anthropic 消息 → 0..n 条 chat 消息:
//   - 纯文本块 → 单条同角色消息(多块换行合并);
//   - assistant 的 text+tool_use 混合 → assistant(text) + assistant(tool_calls) 两条
//     (chat 语义的等价拆分,桥接/quirk 层均按此消费);
//   - user 的 tool_result 块 → role:"tool" 消息(多条 tool_result 各自一条)。
func messageToChat(m *msgMessage) ([]string, error) {
	if m.Role != "user" && m.Role != "assistant" {
		return nil, &MessagesUnsupportedError{Code: CodeInputInvalid}
	}
	// 字符串快捷形态
	if len(m.Content) > 0 && m.Content[0] == '"' {
		var s string
		if json.Unmarshal(m.Content, &s) != nil {
			return nil, &MessagesUnsupportedError{Code: CodeInputInvalid}
		}
		return []string{`{"role":` + string(mustJSON(m.Role)) + `,"content":` + string(mustJSON(s)) + `}`}, nil
	}
	var blocks []msgBlock
	if json.Unmarshal(m.Content, &blocks) != nil {
		return nil, &MessagesUnsupportedError{Code: CodeInputInvalid}
	}

	var msgs []string
	var textParts []string
	var toolCalls []string
	flushText := func() {
		if len(textParts) == 0 {
			return
		}
		msgs = append(msgs, `{"role":`+string(mustJSON(m.Role))+`,"content":`+string(mustJSON(strings.Join(textParts, "\n")))+`}`)
		textParts = nil
	}
	for _, bl := range blocks {
		switch bl.Type {
		case "text":
			textParts = append(textParts, bl.Text)
		case "thinking":
			// 丢弃:v1 不回传签名 thinking 块(登记于设计文档「显式拒绝与丢弃清单」)
		case "tool_use":
			flushText()
			args := "{}"
			if len(bl.Input) > 0 && string(bl.Input) != "null" {
				args = string(bl.Input)
			}
			toolCalls = append(toolCalls, `{"id":`+string(mustJSON(bl.ID))+`,"type":"function","function":{"name":`+string(mustJSON(bl.Name))+`,"arguments":`+string(mustJSON(args))+`}}`)
		case "tool_result":
			flushText()
			txt, err := toolResultToText(bl.Content)
			if err != nil {
				return nil, err
			}
			msgs = append(msgs, `{"role":"tool","tool_call_id":`+string(mustJSON(bl.ToolUseID))+`,"content":`+string(mustJSON(txt))+`}`)
		default:
			return nil, &MessagesUnsupportedError{Code: CodeContentBlockUnsupported}
		}
	}
	if len(toolCalls) > 0 {
		content := "null"
		if len(textParts) > 0 {
			content = string(mustJSON(strings.Join(textParts, "\n")))
		}
		msgs = append(msgs, `{"role":`+string(mustJSON(m.Role))+`,"content":`+content+`,"tool_calls":[`+strings.Join(toolCalls, ",")+`]}`)
		return msgs, nil
	}
	flushText()
	return msgs, nil
}

// toolResultToText tool_result.content(字符串或块数组)→ 文本;image 等拒绝。
func toolResultToText(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return "", &MessagesUnsupportedError{Code: CodeInputInvalid}
		}
		return s, nil
	}
	var blocks []msgBlock
	if json.Unmarshal(raw, &blocks) != nil {
		return "", &MessagesUnsupportedError{Code: CodeInputInvalid}
	}
	var parts []string
	for _, bl := range blocks {
		if bl.Type != "text" {
			return "", &MessagesUnsupportedError{Code: CodeContentBlockUnsupported}
		}
		parts = append(parts, bl.Text)
	}
	return strings.Join(parts, "\n"), nil
}

// translateToolChoice tool_choice → chat 形态:auto/none 直通,any→required,
// 具名 tool → function 嵌套;其余拒绝。
func translateToolChoice(raw json.RawMessage) (string, error) {
	var tc struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	if json.Unmarshal(raw, &tc) != nil {
		return "", &MessagesUnsupportedError{Code: CodeInputInvalid}
	}
	switch tc.Type {
	case "auto":
		return `"auto"`, nil
	case "any":
		return `"required"`, nil
	case "none":
		return `"none"`, nil
	case "tool":
		if tc.Name == "" {
			return "", &MessagesUnsupportedError{Code: CodeInputInvalid}
		}
		return `{"type":"function","function":{"name":` + string(mustJSON(tc.Name)) + `}}`, nil
	default:
		return "", &MessagesUnsupportedError{Code: CodeInputInvalid}
	}
}

// ---- 响应侧映射(非流式) ----

// MessageFromChat chat.completion → Anthropic message 对象。
// msgID = "msg_" + request_id;fallbackModel 供上游未回显 model 时兜底(客户端仅展示用)。
func MessageFromChat(chatBody []byte, msgID, fallbackModel string) ([]byte, error) {
	var chat struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content          *string `json:"content"`
				ReasoningContent *string `json:"reasoning_content"`
				ToolCalls        []struct {
					ID       string `json:"id"`
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
	content := []map[string]any{}
	finish := ""
	if len(chat.Choices) > 0 {
		ch := chat.Choices[0]
		finish = ch.FinishReason
		if ch.Message.ReasoningContent != nil && *ch.Message.ReasoningContent != "" {
			content = append(content, map[string]any{"type": "thinking", "thinking": *ch.Message.ReasoningContent})
		}
		if ch.Message.Content != nil && *ch.Message.Content != "" {
			content = append(content, map[string]any{"type": "text", "text": *ch.Message.Content})
		}
		for _, tc := range ch.Message.ToolCalls {
			var input any
			if json.Unmarshal([]byte(tc.Function.Arguments), &input) != nil || tc.Function.Arguments == "" {
				input = map[string]any{}
			}
			content = append(content, map[string]any{
				"type": "tool_use", "id": tc.ID, "name": tc.Function.Name, "input": input,
			})
		}
	}
	model := chat.Model
	if model == "" {
		model = fallbackModel
	}
	resp := map[string]any{
		"id": msgID, "type": "message", "role": "assistant", "model": model,
		"content":       content,
		"stop_reason":   stopReasonFromChat(finish),
		"stop_sequence": nil,
		"usage":         messagesUsage(chat.Usage),
	}
	return json.Marshal(resp)
}

// stopReasonFromChat chat finish_reason → Anthropic stop_reason。
func stopReasonFromChat(finish string) any {
	switch finish {
	case "stop":
		return "end_turn"
	case "length":
		return "max_tokens"
	case "tool_calls":
		return "tool_use"
	default:
		return nil
	}
}

// messagesUsage chat usage → Anthropic usage(cached 命中入 cache_read_input_tokens;
// reasoning_tokens 是 completion 子集,不另立字段)。
func messagesUsage(u *Usage) map[string]int64 {
	out := map[string]int64{"input_tokens": 0, "output_tokens": 0}
	if u == nil {
		return out
	}
	out["input_tokens"] = u.PromptTokens
	out["output_tokens"] = u.CompletionTokens
	if u.CachedTokens > 0 {
		out["cache_read_input_tokens"] = u.CachedTokens
	}
	return out
}

// ---- 流式事件编码器 ----

// msgToolBlock 流式工具块的累积态(按上游 tool_calls[].index 跟踪,并行工具各自独立)。
type msgToolBlock struct {
	blockIndex int
	callID     string
	name       string
	args       strings.Builder
	started    bool
}

// MessagesStreamEncoder 把上游 OpenAI 形态 SSE 帧翻译为 Anthropic 事件序列
// (顺序不变式:message_start 首帧、message_stop 终帧、content_block_* 仅出现于两者之间;
// 块索引按发出顺序递增,文本/思考/工具切换时先收束前一块)。
// 已知偏差(登记于设计文档):message_start.usage.input_tokens 恒 0(上游 usage 在流尾才知),
// 真实用量在 message_delta.usage 补全。非并发安全:单请求单实例。
type MessagesStreamEncoder struct {
	id, model  string
	started    bool
	done       bool
	nextIndex  int
	textOpen   int // 打开中的 text 块索引;-1 = 未打开
	thinkOpen  int // 打开中的 thinking 块索引;-1 = 未打开
	tools      map[int]*msgToolBlock
	toolOrder  []int
	stopReason string
	usage      *Usage
}

// NewMessagesStreamEncoder 构造;id 应传 msg_<request_id>。
func NewMessagesStreamEncoder(id, model string) *MessagesStreamEncoder {
	return &MessagesStreamEncoder{
		id: id, model: model, textOpen: -1, thinkOpen: -1,
		tools: map[int]*msgToolBlock{}, stopReason: "end_turn",
	}
}

func (e *MessagesStreamEncoder) event(v map[string]any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// Init 首帧 message_start(在任一内容事件之前恰好调用一次)。
func (e *MessagesStreamEncoder) Init() []byte {
	e.started = true
	return e.event(map[string]any{"type": "message_start", "message": map[string]any{
		"id": e.id, "type": "message", "role": "assistant", "model": e.model,
		"content": []any{}, "stop_reason": nil, "stop_sequence": nil,
		"usage": map[string]int64{"input_tokens": 0, "output_tokens": 0},
	}})
}

// ensureStarted 惰性补发首帧。
func (e *MessagesStreamEncoder) ensureStarted(out *[][]byte) {
	if !e.started {
		*out = append(*out, e.Init())
	}
}

// closeBlock 收束打开中的内容块(content_block_stop)。
func (e *MessagesStreamEncoder) closeBlock(out *[][]byte) {
	if e.textOpen >= 0 {
		*out = append(*out, e.event(map[string]any{"type": "content_block_stop", "index": e.textOpen}))
		e.textOpen = -1
	}
	if e.thinkOpen >= 0 {
		*out = append(*out, e.event(map[string]any{"type": "content_block_stop", "index": e.thinkOpen}))
		e.thinkOpen = -1
	}
}

// Feed 消费一帧上游 OpenAI chunk,返回待写的 Anthropic 事件序列。
// usage 恒捕获(finish 帧与独立 usage 帧均兼容);仅消费 Index==0 或缺省的 choice。
func (e *MessagesStreamEncoder) Feed(chunk []byte) [][]byte {
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
	e.ensureStarted(&out)
	for _, ch := range c.Choices {
		if ch.Index != nil && *ch.Index != 0 {
			continue // 仅映射首 choice(契约未定义多 choice 映射,防御性忽略)
		}
		if ch.Delta.ReasoningContent != nil && *ch.Delta.ReasoningContent != "" {
			e.closeBlock(&out) // 思考与文本互斥出现(anthropic 上游同款语义),切换前收束
			if e.thinkOpen < 0 {
				e.thinkOpen = e.nextIndex
				e.nextIndex++
				out = append(out, e.event(map[string]any{"type": "content_block_start",
					"index": e.thinkOpen, "content_block": map[string]any{"type": "thinking", "thinking": ""}}))
			}
			out = append(out, e.event(map[string]any{"type": "content_block_delta",
				"index": e.thinkOpen, "delta": map[string]any{"type": "thinking_delta", "thinking": *ch.Delta.ReasoningContent}}))
		}
		if ch.Delta.Content != nil && *ch.Delta.Content != "" {
			e.closeBlock(&out)
			if e.textOpen < 0 {
				e.textOpen = e.nextIndex
				e.nextIndex++
				out = append(out, e.event(map[string]any{"type": "content_block_start",
					"index": e.textOpen, "content_block": map[string]any{"type": "text", "text": ""}}))
			}
			out = append(out, e.event(map[string]any{"type": "content_block_delta",
				"index": e.textOpen, "delta": map[string]any{"type": "text_delta", "text": *ch.Delta.Content}}))
		}
		for _, raw := range ch.Delta.ToolCalls {
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
			tb, ok := e.tools[idx]
			if !ok {
				tb = &msgToolBlock{blockIndex: -1}
				e.tools[idx] = tb
				e.toolOrder = append(e.toolOrder, idx)
			}
			if tc.ID != "" {
				tb.callID = tc.ID
			}
			if tc.Function.Name != "" {
				tb.name = tc.Function.Name
			}
			if !tb.started {
				// call_id/name 允许后续帧补齐(args 先到不丢——与 Responses 编码器同款防御)
				e.closeBlock(&out)
				tb.blockIndex = e.nextIndex
				e.nextIndex++
				tb.started = true
				out = append(out, e.event(map[string]any{"type": "content_block_start",
					"index": tb.blockIndex, "content_block": map[string]any{
						"type": "tool_use", "id": tb.callID, "name": tb.name, "input": map[string]any{}}}))
			}
			if tc.Function.Arguments != "" {
				tb.args.WriteString(tc.Function.Arguments)
				out = append(out, e.event(map[string]any{"type": "content_block_delta",
					"index": tb.blockIndex, "delta": map[string]any{"type": "input_json_delta", "partial_json": tc.Function.Arguments}}))
			}
		}
		if ch.FinishReason != nil && *ch.FinishReason != "" && !e.done {
			if sr := stopReasonFromChat(*ch.FinishReason); sr != nil {
				e.stopReason = sr.(string)
			}
		}
	}
	if c.Usage != nil {
		e.usage = c.Usage
	}
	return out
}

// Complete 终态两连发:message_delta(stop_reason+usage) + message_stop(仅应调用一次)。
func (e *MessagesStreamEncoder) Complete() [][]byte {
	var out [][]byte
	e.ensureStarted(&out)
	e.closeBlock(&out)
	for _, idx := range e.toolOrder {
		if tb := e.tools[idx]; tb != nil && tb.started {
			out = append(out, e.event(map[string]any{"type": "content_block_stop", "index": tb.blockIndex}))
		}
	}
	out = append(out, e.event(map[string]any{"type": "message_delta",
		"delta": map[string]any{"stop_reason": e.stopReason, "stop_sequence": nil},
		"usage": messagesUsage(e.usage)}))
	out = append(out, e.event(map[string]any{"type": "message_stop"}))
	e.done = true
	return out
}

// Failed 流中失败:error 事件(诚实失败,不伪造 message_stop)。
func (e *MessagesStreamEncoder) Failed(message string) []byte {
	return e.event(map[string]any{"type": "error",
		"error": map[string]any{"type": "api_error", "message": message}})
}
