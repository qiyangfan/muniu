// Responses API 入站翻译(ADR-0012 无状态 v1;契约=proxy-and-streaming.md「Responses 入站翻译」):
// 边缘翻译为规范 chat 请求后复用既有管道;响应与流式事件反向映射。
// 全程零 float;翻译失败以 ResponsesUnsupportedError 携带机器码,handler 映射 400。
package protocol

import (
	"encoding/json"
	"fmt"
	"strings"
)

// 显式拒绝机器码(设计文档「显式拒绝清单」;禁散落硬编码)。
const (
	CodePreviousResponseIDUnsupported = "previous_response_id_unsupported"
	CodeBackgroundUnsupported         = "background_unsupported"
	CodeInputItemUnsupported          = "input_item_unsupported"
	CodeInputInvalid                  = "input_invalid"
)

// ResponsesUnsupportedError 显式拒绝(危险字段/不支持项);handler 映射 400 + code。
type ResponsesUnsupportedError struct{ Code string }

func (e *ResponsesUnsupportedError) Error() string { return "responses: " + e.Code }

// respPart content part:仅文本类可翻译;其余类型显式拒绝。
type respPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// respItem input[] 项:仅 message/function_call/function_call_output 三类。
type respItem struct {
	Type    string     `json:"type"`
	Role    string     `json:"role"`
	Content []respPart `json:"content"`
	// function_call
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	// function_call_output
	Output string `json:"output"`
}

// respTool Responses 顶层 function 工具(name 在顶层,chat 需移入 function)。
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

// TranslateResponsesRequest Responses 请求 → 规范 chat 请求(映射表见设计文档,fixture 钉死)。
// 返回的 ChatRequest.Raw 即 canonical chat JSON 字节——治理/缓存/计量与等价 chat 请求同构。
func TranslateResponsesRequest(body []byte) (*ChatRequest, error) {
	var rr responsesRequest
	if err := json.Unmarshal(body, &rr); err != nil {
		return nil, &ResponsesUnsupportedError{Code: CodeInputInvalid}
	}
	if rr.PreviousResponseID != nil {
		return nil, &ResponsesUnsupportedError{Code: CodePreviousResponseIDUnsupported}
	}
	if rr.Background != nil && *rr.Background {
		return nil, &ResponsesUnsupportedError{Code: CodeBackgroundUnsupported}
	}
	if rr.Model == "" || len(rr.Input) == 0 {
		return nil, &ResponsesUnsupportedError{Code: CodeInputInvalid}
	}

	var b strings.Builder
	b.WriteString(`{"model":`)
	b.Write(mustJSON(rr.Model))

	var msgs []string
	if rr.Instructions != "" {
		msgs = append(msgs, `{"role":"system","content":`+string(mustJSON(rr.Instructions))+`}`)
	}
	switch rr.Input[0] {
	case '"': // string 快捷形态 → 单条 user 消息
		msgs = append(msgs, `{"role":"user","content":`+string(rr.Input)+`}`)
	case '[':
		var items []respItem
		if err := json.Unmarshal(rr.Input, &items); err != nil {
			return nil, &ResponsesUnsupportedError{Code: CodeInputInvalid}
		}
		for _, it := range items {
			switch it.Type {
			case "message":
				text, err := partsToText(it.Content)
				if err != nil {
					return nil, err
				}
				role := it.Role
				if role == "" {
					role = "user"
				}
				msgs = append(msgs, `{"role":`+string(mustJSON(role))+`,"content":`+string(mustJSON(text))+`}`)
			case "function_call":
				args := it.Arguments
				if args == "" {
					args = "{}"
				}
				msgs = append(msgs, `{"role":"assistant","content":null,"tool_calls":[{"id":`+string(mustJSON(it.CallID))+`,"type":"function","function":{"name":`+string(mustJSON(it.Name))+`,"arguments":`+string(mustJSON(args))+`}}]}`)
			case "function_call_output":
				msgs = append(msgs, `{"role":"tool","tool_call_id":`+string(mustJSON(it.CallID))+`,"content":`+string(mustJSON(it.Output))+`}`)
			default:
				return nil, &ResponsesUnsupportedError{Code: CodeInputItemUnsupported}
			}
		}
	default:
		return nil, &ResponsesUnsupportedError{Code: CodeInputInvalid}
	}

	b.WriteString(`,"messages":[` + strings.Join(msgs, ",") + `]`)
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
	if len(rr.Tools) > 0 {
		out := make([]map[string]any, 0, len(rr.Tools))
		for _, tl := range rr.Tools {
			if tl.Type != "function" {
				return nil, &ResponsesUnsupportedError{Code: CodeInputItemUnsupported}
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
		b.WriteString(`,"tools":` + string(tj))
	}
	if len(rr.ToolChoice) > 0 && string(rr.ToolChoice) != "null" {
		var tc any
		if json.Unmarshal(rr.ToolChoice, &tc) != nil {
			return nil, &ResponsesUnsupportedError{Code: CodeInputInvalid}
		}
		if m, isM := tc.(map[string]any); isM {
			// Responses 具名形态 {"type":"function","name":x} → chat 的 {"type":"function","function":{"name":x}}
			// (域A 审查:此前条件 m["type"]==nil 写反,规范输入被原样透传为非法 chat)
			if name, isS := m["name"].(string); isS {
				fn := map[string]any{"name": name}
				if desc, has := m["description"].(string); has {
					fn["description"] = desc
				}
				tc = map[string]any{"type": "function", "function": fn}
			} else if m["function"] == nil {
				return nil, &ResponsesUnsupportedError{Code: CodeInputItemUnsupported}
			}
		}
		tj, _ := json.Marshal(tc)
		b.WriteString(`,"tool_choice":` + string(tj))
	}
	b.WriteString(`}`)

	// canonical JSON 经既有解析器回填 typed 字段(单源,避免两套解析)
	return ParseChatRequest([]byte(b.String()))
}

// partsToText content parts → 文本(input_text/output_text;其余类型显式拒绝)。
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

// mustJSON 字符串 → JSON 字串字面量(含转义)。
func mustJSON(s string) []byte {
	b, _ := json.Marshal(s)
	return b
}

// ---- 响应侧映射 ----

// responsesUsage chat usage → Responses usage(input/output/total;cached/reasoning 入 details)。
type responsesUsage struct {
	InputTokens         int64            `json:"input_tokens"`
	InputTokensDetails  map[string]int64 `json:"input_tokens_details,omitempty"`
	OutputTokens        int64            `json:"output_tokens"`
	OutputTokensDetails map[string]int64 `json:"output_tokens_details,omitempty"`
	TotalTokens         int64            `json:"total_tokens"`
}

// ResponsesObjectFromChat 非流式:chat.completion → Responses 对象。
// respID = "resp_" + request_id(追责链路与日志/usage_logs 一致)。
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
		// output_tokens = completion_tokens:reasoning_tokens 是其子集(OpenAI/DeepSeek details 语义),
		// 相加即客户端可见双计——reasoning 只入 details
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

// toolItem 单个 function_call 工具项的流式累积态(按上游 tool_calls[].index 跟踪,
// 并行多工具各自独立 added/args/done,杜绝参数与 call_id 错配)。
type toolItem struct {
	added  bool
	itemID string // item_1 起(0 号留给文本);并行工具各自独立
	callID string
	name   string
	args   strings.Builder
}

// ResponsesStreamEncoder 把上游 OpenAI 形态 SSE 帧翻译为 Responses 最小事件集
// (顺序不变式:created 首帧、completed 终帧、delta 仅出现于两者之间)。
// item 跟踪:文本单 item;function_call 按上游内层 index 多 item 独立配对;
// 文本与工具混流时先收束文本再开启工具;仅消费 StreamChoice.Index==0 或缺省的 choice
// (v1 契约未定义多 choice 映射,其余防御性忽略)。非并发安全:单请求单实例。
type ResponsesStreamEncoder struct {
	id, model string
	created   bool
	msgOpen   bool
	textOut   strings.Builder
	tools     map[int]*toolItem
	toolOrder []int
	usage     *Usage
	done      bool
}

// NewResponsesStreamEncoder 构造;id 应传 resp_<request_id>。
func NewResponsesStreamEncoder(id, model string) *ResponsesStreamEncoder {
	return &ResponsesStreamEncoder{id: id, model: model, tools: map[int]*toolItem{}}
}

func (e *ResponsesStreamEncoder) event(v map[string]any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// Init 首帧 response.created(在任一 delta 之前恰好调用一次)。
func (e *ResponsesStreamEncoder) Init() []byte {
	e.created = true
	return e.event(map[string]any{"type": "response.created", "response": map[string]any{
		"id": e.id, "object": "response", "status": "in_progress", "model": e.model, "output": []any{},
	}})
}

// ensureCreated 惰性补发首帧。
func (e *ResponsesStreamEncoder) ensureCreated(out *[][]byte) {
	if !e.created {
		*out = append(*out, e.Init())
	}
}

// closeMessage 收束文本 item(msgOpen 时发 done 并复位;否则 no-op)。
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

// Feed 消费一帧上游 OpenAI chunk(delta/usage/finish),返回待写的 Responses 事件序列。
// usage 恒捕获(finish 帧与独立 usage 帧均兼容);reasoning_content 流式不透出
// (v1 已登记限制:最小事件集无 reasoning 事件类型,非流式路径只读透出)。
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
			continue // v1 仅映射首 choice(防御多 choice 合流错乱;契约未定义多 choice 映射)
		}
		if ch.Delta.Content != nil && *ch.Delta.Content != "" {
			if !e.msgOpen {
				e.closeMessage(&out) // 文本与工具混流:切换前收束文本
			}
			if !e.msgOpen {
				out = append(out, e.event(map[string]any{"type": "response.output_item.added",
					"output_index": 0, "item": map[string]any{"type": "message", "role": "assistant",
						"status": "in_progress", "content": []any{}}}))
				e.msgOpen = true
			}
			e.textOut.WriteString(*ch.Delta.Content)
			out = append(out, e.event(map[string]any{
				"type": "response.output_text.delta", "item_id": "item_0", "output_index": 0, "content_index": 0,
				"delta": *ch.Delta.Content,
			}))
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
				// 有 tool_call 增量即开启 item(call_id/name 允许后续帧补齐,
				// 杜绝「args 先于 name 到达」的非规范上游丢参数——域A 审查 B 项)
				ti.added = true
				out = append(out, e.event(map[string]any{"type": "response.output_item.added",
					"item_id": ti.itemID, "output_index": 0, "item": map[string]any{"type": "function_call",
						"call_id": ti.callID, "name": ti.name, "arguments": "", "status": "in_progress"}}))
			}
			if tc.Function.Arguments != "" && ti.added {
				ti.args.WriteString(tc.Function.Arguments)
				out = append(out, e.event(map[string]any{
					"type": "response.function_call_arguments.delta", "item_id": ti.itemID, "output_index": 0,
					"delta": tc.Function.Arguments,
				}))
			}
		}
		if ch.FinishReason != nil && *ch.FinishReason != "" && !e.done {
			e.done = true
			e.closeMessage(&out)
			for _, idx := range e.toolOrder {
				if ti := e.tools[idx]; ti != nil && ti.added {
					out = append(out, e.event(map[string]any{
						"type": "response.output_item.done", "output_index": 0,
						"item": map[string]any{"type": "function_call", "call_id": ti.callID,
							"name": ti.name, "arguments": ti.args.String(), "status": "completed"}}))
				}
			}
		}
	}
	if c.Usage != nil {
		e.usage = c.Usage
	}
	return out
}

// Complete 终帧 response.completed(usage 恒携带,无上游 usage 时补零值;仅应调用一次)。
func (e *ResponsesStreamEncoder) Complete() []byte {
	e.created = true
	u := responsesUsage{}
	if e.usage != nil {
		// output_tokens = completion_tokens:reasoning_tokens 是其子集(OpenAI/DeepSeek details
		// 语义),相加即客户端可见双计——reasoning 只入 details(契约修正,域A/B 审查确认)
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

// Failed 流中失败:response.failed(诚实失败,不伪造 completed)。
func (e *ResponsesStreamEncoder) Failed(message string) []byte {
	return e.event(map[string]any{"type": "response.failed", "response": map[string]any{
		"id": e.id, "object": "response", "status": "failed", "error": map[string]any{"message": message},
	}})
}
