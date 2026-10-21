// Package protocol 定义入站与出站共享的 DTO：Chat 请求/响应、SSE chunk、usage 与错误信封。
// 本包只依赖标准库，不 import 任何内部包。解析只取网关行为所需字段，其余经
// json.RawMessage 原样持有，适配层改写始终基于完整原始字节，避免字段丢失。
package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
)

// ChatRequest 入站 OpenAI Chat Completions 请求。
// 只解析网关需要的字段：路由、流式判定、校验、缓存准入；Raw 保留完整原始字节，
// 适配器转换基于 Raw 进行，规避 request body 不可重读问题。
type ChatRequest struct {
	Model       string          `json:"model"`
	Stream      bool            `json:"stream"`
	Messages    []Message       `json:"messages"`
	Temperature *float64        `json:"temperature,omitempty"`
	MaxTokens   *int64          `json:"max_tokens,omitempty"`
	Tools       json.RawMessage `json:"tools,omitempty"` // 仅用于准入判定，语义缓存排除带工具请求；出站仍走 Raw
	Raw         json.RawMessage `json:"-"`
}

// Message 消息条目。Content 可能是字符串或多模态数组，网关不做深解析，原样透传。
type Message struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content,omitempty"`
}

// ChatResponse 非流式响应，统一 OpenAI 形态。Usage 保留原始字节，归一化必须经适配器
// quirk 完成——各家缓存与 reasoning 字段位置不同，禁止在此直接结构化。
type ChatResponse struct {
	ID      string          `json:"id,omitempty"`
	Model   string          `json:"model,omitempty"`
	Choices []Choice        `json:"choices,omitempty"`
	Usage   json.RawMessage `json:"usage,omitempty"`
	Raw     json.RawMessage `json:"-"`
}

// Choice 非流式候选项。
type Choice struct {
	Index        int             `json:"index,omitempty"`
	Message      ResponseMessage `json:"message,omitempty"`
	FinishReason string          `json:"finish_reason,omitempty"`
}

// ResponseMessage 助手回复消息，content 兼容字符串与数组，原样持有。
type ResponseMessage struct {
	Role    string          `json:"role,omitempty"`
	Content json.RawMessage `json:"content,omitempty"`
}

// StreamChunk 流式 SSE data 帧的 OpenAI 形态，用于逐事件解析、计量与首 token 判定。
// 透传仍用原始字节，字段改写走白名单，兼容型供应商客户端可见行为零改写。
type StreamChunk struct {
	ID      string         `json:"id,omitempty"`
	Choices []StreamChoice `json:"choices,omitempty"`
	Usage   *Usage         `json:"usage,omitempty"`
}

// StreamChoice 单个流式候选：delta 增量 + 终止原因。
type StreamChoice struct {
	Delta        Delta             `json:"delta"`
	Index        *int              `json:"index,omitempty"` // v1 仅消费 index 0，responses 流式映射的防御性过滤
	FinishReason *string           `json:"finish_reason,omitempty"`
	ToolCalls    []json.RawMessage `json:"tool_calls,omitempty"`
}

// Delta 流式增量。reasoning_content 是 DeepSeek R1 等国产系的事实字段，统一在此识别。
type Delta struct {
	Role             string            `json:"role,omitempty"`
	Content          *string           `json:"content,omitempty"`
	ReasoningContent *string           `json:"reasoning_content,omitempty"`
	ToolCalls        []json.RawMessage `json:"tool_calls,omitempty"`
}

// Usage 统一 usage 形态，metering 归一化的对齐基准；缺失字段为 0。
type Usage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	ReasoningTokens  int64 `json:"reasoning_tokens,omitempty"`
	CachedTokens     int64 `json:"cached_tokens,omitempty"`
}

// ErrInvalidRequest 入站请求不合法：model 缺失、messages 非法或非 JSON。
// 哨兵错误，出口统一映射 bad_request。
var ErrInvalidRequest = errors.New("protocol: 非法入站请求")

// ParseChatRequest 解析并校验入站请求：model 非空、messages 为非空数组且每条有 role。
func ParseChatRequest(body []byte) (*ChatRequest, error) {
	var req ChatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, errors.Join(ErrInvalidRequest, err)
	}
	if req.Model == "" {
		return nil, errors.Join(ErrInvalidRequest, errors.New("model 不能为空"))
	}
	if len(req.Messages) == 0 {
		return nil, errors.Join(ErrInvalidRequest, errors.New("messages 不能为空"))
	}
	for i, m := range req.Messages {
		if m.Role == "" {
			return nil, errors.Join(ErrInvalidRequest, errors.New("messages["+itoa(i)+"] 缺 role"))
		}
	}
	req.Raw = append(json.RawMessage(nil), body...)
	return &req, nil
}

// ParseStreamChunk 解析单个 SSE data 帧为 OpenAI 形态，解析失败即上游协议异常，由调用方归类处理。
func ParseStreamChunk(data []byte) (*StreamChunk, error) {
	var c StreamChunk
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// HasContent 首 token 边界判定：content、reasoning_content、tool_call 增量任一非空即算，
// role 帧、空 delta、usage 终帧均不算。
func (c *StreamChunk) HasContent() bool {
	for _, ch := range c.Choices {
		d := ch.Delta
		if (d.Content != nil && *d.Content != "") ||
			(d.ReasoningContent != nil && *d.ReasoningContent != "") ||
			len(d.ToolCalls) > 0 || len(ch.ToolCalls) > 0 {
			return true
		}
	}
	return false
}

// IsUsageOnly 判定是否 usage 终帧：无 choices 且带 usage。
func (c *StreamChunk) IsUsageOnly() bool {
	return len(c.Choices) == 0 && c.Usage != nil
}

// RewriteModel 出站 body 生成，字段改写白名单的唯一写入口：以 map[string]json.RawMessage
// 解原文，只替换 model 后重编，未知字段零丢失。字段顺序不保证与原文一致，仅保证字段集合等价。
func RewriteModel(body []byte, upstreamModel string) ([]byte, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return nil, errors.Join(ErrInvalidRequest, err)
	}
	fields["model"], _ = json.Marshal(upstreamModel)
	out, err := json.Marshal(fields)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// itoa 极小整数转字符串，避免引入 fmt 依赖，仅错误信息使用。
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(b[pos:])
}

// RewriteModelInPlace 顶层 model 字段的原位替换：仅改该字段值字节，键序、空白、转义风格
// 与未知字段逐字节保留。RewriteModel 的 map 往返会重排键序，不适用于直通 wire。
// 顶层非对象或缺 model 字段报错。
func RewriteModelInPlace(body []byte, upstreamModel string) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	tok, err := dec.Token()
	if err != nil || (func() bool { d, ok := tok.(json.Delim); return !ok || d != '{' })() {
		return nil, errors.Join(ErrInvalidRequest, errors.New("model: 顶层须为 JSON 对象"))
	}
	vs, ve := -1, -1
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, errors.Join(ErrInvalidRequest, err)
		}
		key, _ := keyTok.(string)
		offBefore := dec.InputOffset()
		valTok, err := dec.Token()
		if err != nil {
			return nil, errors.Join(ErrInvalidRequest, err)
		}
		offAfter := dec.InputOffset()
		if key == "model" {
			if _, isStr := valTok.(string); !isStr {
				return nil, errors.Join(ErrInvalidRequest, errors.New("model: 须为字符串"))
			}
			vs, ve = int(offBefore), int(offAfter)
		}
	}
	if _, err := dec.Token(); err != nil { // 收尾 '}'
		return nil, errors.Join(ErrInvalidRequest, err)
	}
	if vs < 0 {
		return nil, errors.Join(ErrInvalidRequest, errors.New("model: 原始体缺 model 字段"))
	}
	// 值起点为跳过 key 后的空白、冒号与空白，终点为 Token 读毕位置，即值最后一字节之后
	for vs < ve && (body[vs] == ' ' || body[vs] == '\t' || body[vs] == '\n' || body[vs] == '\r') {
		vs++
	}
	if vs < ve && body[vs] == ':' {
		vs++
	}
	for vs < ve && (body[vs] == ' ' || body[vs] == '\t' || body[vs] == '\n' || body[vs] == '\r') {
		vs++
	}
	newVal, _ := json.Marshal(upstreamModel)
	out := make([]byte, 0, len(body)-(ve-vs)+len(newVal))
	out = append(out, body[:vs]...)
	out = append(out, newVal...)
	out = append(out, body[ve:]...)
	return out, nil
}
