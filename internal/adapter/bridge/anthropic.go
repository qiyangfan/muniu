// Package bridge 承载异构协议桥接(ADR-0003):Anthropic Messages ↔ OpenAI 形态全翻译。
// 桥接是唯一允许改写客户端可见字段的适配器(upstream-adapters「字段改写白名单」)。
// 翻译矩阵见 bridge/README.md;源契约:Anthropic Messages API(2023-06-01 版本头,公开稳定形态)。
package bridge

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/solvela/muniu/internal/adapter"
	"github.com/solvela/muniu/internal/failover"
	"github.com/solvela/muniu/internal/protocol"
)

// 编译期契约断言:桥接实现 adapter.Adapter。
var _ adapter.Adapter = (*Anthropic)(nil)

// init 向 adapter 注册桥接工厂(应用方 blank import 本包即完成接线)。
func init() {
	adapter.SetAnthropicFactory(func(baseURL string, opts adapter.Options) adapter.Adapter {
		return NewAnthropic(baseURL, opts)
	})
}

// anthropicVersion 版本头(公开稳定值;文档区域屏蔽,按公开契约保守实现,见 HANDOFF)。
const anthropicVersion = "2023-06-01"

// Anthropic 桥接实例(基址绑定;key 由 keypool 每请求提供)。
type Anthropic struct {
	baseURL string
	bearer  bool // true=凭证走 Authorization: Bearer(国内分支官方文档口径);缺省 x-api-key(协议标准)
}

// NewAnthropic 构造桥接。opts.Auth=adapter.AuthBearer 时按声明切换鉴权头。
func NewAnthropic(baseURL string, opts adapter.Options) *Anthropic {
	return &Anthropic{baseURL: strings.TrimRight(baseURL, "/"), bearer: opts.Auth == adapter.AuthBearer}
}

// Prepare OpenAI Chat 请求 → Anthropic /v1/messages 请求。
// 翻译要点:system 提升为顶层;max_tokens 必填(缺省 4096);tools 形态重排;
// 连续同角色消息合并(Anthropic 要求 user/assistant 交替)。
func (a *Anthropic) Prepare(in *protocol.ChatRequest, upstreamModel, apiKey string) (*adapter.Outbound, error) {
	body, err := a.buildRequest(in, upstreamModel)
	if err != nil {
		return nil, fmt.Errorf("bridge: 翻译请求: %w", err)
	}
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	if a.bearer {
		// 国内分支声明口径:与其官方文档 AUTH_TOKEN(Bearer)形态一致
		h.Set("Authorization", "Bearer "+apiKey)
	} else {
		// 协议标准形态(官方 Anthropic 与官方 SDK 缺省)
		h.Set("x-api-key", apiKey)
	}
	h.Set("anthropic-version", anthropicVersion)
	return &adapter.Outbound{
		URL:    a.baseURL + "/v1/messages",
		Header: h,
		Body:   body,
	}, nil
}

// buildRequest 请求体翻译。
func (a *Anthropic) buildRequest(in *protocol.ChatRequest, upstreamModel string) ([]byte, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(in.Raw, &fields); err != nil {
		return nil, err
	}
	maxTokens := int64(4096)
	if in.MaxTokens != nil && *in.MaxTokens > 0 {
		maxTokens = *in.MaxTokens
	}

	// 块级内部形态:aBlock 单结构分族承载 text/tool_use/tool_result(omitempty 分族序列化)。
	// 发射期:全 text 块 → content 字符串(多块 \n\n 连接,既有合并语义逐字节保持);
	// 含非 text 块 → content 块数组(工具上下文形态)。
	type aBlock struct {
		Type      string          `json:"type"`
		Text      string          `json:"text,omitempty"`
		ID        string          `json:"id,omitempty"`
		Name      string          `json:"name,omitempty"`
		Input     json.RawMessage `json:"input,omitempty"`
		ToolUseID string          `json:"tool_use_id,omitempty"`
		Content   string          `json:"content,omitempty"`
	}
	type msgAcc struct {
		Role   string
		Blocks []aBlock
	}
	// Raw 信封解析(Message 结构不携带 tool_calls/tool_call_id——与 gemini 适配器同缝)
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

	var systemParts []string
	var msgs []msgAcc
	seenToolUse := map[string]bool{}
	// 追加一条消息(与尾消息同角色则块级合并,满足 Anthropic 交替角色约束)
	appendMsg := func(role string, blocks []aBlock) {
		if len(msgs) > 0 && msgs[len(msgs)-1].Role == role {
			msgs[len(msgs)-1].Blocks = append(msgs[len(msgs)-1].Blocks, blocks...)
			return
		}
		msgs = append(msgs, msgAcc{Role: role, Blocks: blocks})
	}
	// role=tool 的 pending 缓冲:tool_result 必须并入单条 user 消息,防连续 user 断裂交替
	var pendingResults []aBlock
	flushResults := func() {
		if len(pendingResults) == 0 {
			return
		}
		appendMsg("user", pendingResults)
		pendingResults = nil
	}

	for i := range env.Messages {
		m := &env.Messages[i]
		text, err := contentText(m.Content)
		if err != nil {
			return nil, fmt.Errorf("messages[%d]: %w", i, err)
		}
		switch m.Role {
		case "system", "developer":
			if text != "" {
				systemParts = append(systemParts, text)
			}
		case "tool":
			// tool_use_id 必须回指本对话已见的 assistant.tool_calls:未配对属结构性非法
			if m.ToolCallID == "" || !seenToolUse[m.ToolCallID] {
				return nil, fmt.Errorf("messages[%d]: role=tool 的 tool_call_id %q 找不到配对的 assistant.tool_calls", i, m.ToolCallID)
			}
			pendingResults = append(pendingResults, aBlock{Type: "tool_result", ToolUseID: m.ToolCallID, Content: text})
		case "user":
			flushResults()
			var blocks []aBlock
			if text != "" {
				blocks = append(blocks, aBlock{Type: "text", Text: text})
			}
			appendMsg("user", blocks)
		case "assistant":
			flushResults()
			var blocks []aBlock
			if text != "" {
				blocks = append(blocks, aBlock{Type: "text", Text: text})
			}
			for _, tc := range m.ToolCalls {
				input := json.RawMessage(tc.Function.Arguments)
				if len(input) == 0 || string(input) == "null" {
					input = json.RawMessage("{}")
				}
				blocks = append(blocks, aBlock{Type: "tool_use", ID: tc.ID, Name: tc.Function.Name, Input: input})
				seenToolUse[tc.ID] = true
			}
			appendMsg("assistant", blocks)
		default:
			continue // 未知角色不在桥接范围,丢弃(既有纪律)
		}
	}
	flushResults()

	type wireMsg struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	wireMsgs := make([]wireMsg, 0, len(msgs))
	for _, m := range msgs {
		texts := make([]string, 0, len(m.Blocks))
		hasNonText := false
		for _, b := range m.Blocks {
			if b.Type == "text" {
				texts = append(texts, b.Text)
			} else {
				hasNonText = true
			}
		}
		var content json.RawMessage
		if hasNonText {
			content, _ = json.Marshal(m.Blocks)
		} else {
			// 全 text(含空块列表):字符串发射,纯文本 wire 与既有行为逐字节一致
			content, _ = json.Marshal(strings.Join(texts, "\n\n"))
		}
		wireMsgs = append(wireMsgs, wireMsg{Role: m.Role, Content: content})
	}

	out := map[string]json.RawMessage{
		"model":      json.RawMessage(mustJSON(upstreamModel)),
		"max_tokens": json.RawMessage(mustJSON(maxTokens)),
		"messages":   json.RawMessage(mustJSON(wireMsgs)),
	}
	if in.Stream {
		out["stream"] = json.RawMessage("true")
	}
	if len(systemParts) > 0 {
		out["system"] = json.RawMessage(mustJSON(strings.Join(systemParts, "\n\n")))
	}
	if in.Temperature != nil {
		out["temperature"] = json.RawMessage(mustJSON(*in.Temperature))
	}
	// tools:OpenAI function 形态 → Anthropic input_schema 形态
	if raw, ok := fields["tools"]; ok {
		var oaiTools []struct {
			Type     string `json:"type"`
			Function struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				Parameters  json.RawMessage `json:"parameters"`
			} `json:"function"`
		}
		// 解析失败必须报错(归入站 bad_request):静默吞掉会让上游在无工具状态下作答且无痕迹
		if err := json.Unmarshal(raw, &oaiTools); err != nil {
			return nil, fmt.Errorf("tools 形态非法(须为 function 数组): %w", err)
		}
		if len(oaiTools) > 0 {
			type aTool struct {
				Name        string          `json:"name"`
				Description string          `json:"description,omitempty"`
				InputSchema json.RawMessage `json:"input_schema"`
			}
			tools := make([]aTool, 0, len(oaiTools))
			for _, tl := range oaiTools {
				if tl.Type != "function" || tl.Function.Name == "" {
					continue
				}
				schema := tl.Function.Parameters
				if len(schema) == 0 {
					schema = json.RawMessage(`{"type":"object"}`)
				}
				tools = append(tools, aTool{Name: tl.Function.Name, Description: tl.Function.Description, InputSchema: schema})
			}
			if len(tools) > 0 {
				out["tools"] = json.RawMessage(mustJSON(tools))
			}
		}
	}
	// top_p 原样透传(Anthropic 支持同形态采样参数)
	if raw, ok := fields["top_p"]; ok {
		out["top_p"] = raw
	}
	// tool_choice:OpenAI 枚举/具名形态 → Anthropic 形态(含 none,冒烟实证)
	if raw, ok := fields["tool_choice"]; ok {
		var tc struct {
			Type     string `json:"type"`
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		}
		switch {
		case json.Unmarshal(raw, &tc) == nil && tc.Type == "auto":
			out["tool_choice"] = json.RawMessage(`{"type":"auto"}`)
		case json.Unmarshal(raw, &tc) == nil && tc.Type == "none":
			// Anthropic none 形态(禁用工具强制纯文本作答);真上游冒烟实证接受
			out["tool_choice"] = json.RawMessage(`{"type":"none"}`)
		case json.Unmarshal(raw, &tc) == nil && tc.Type == "required":
			out["tool_choice"] = json.RawMessage(`{"type":"any"}`)
		case json.Unmarshal(raw, &tc) == nil && tc.Type == "function" && tc.Function.Name != "":
			out["tool_choice"] = json.RawMessage(mustJSON(map[string]string{"type": "tool", "name": tc.Function.Name}))
		}
	}
	return json.Marshal(out)
}

// Classify 按状态码粗分(与基座同表;Anthropic 529 overloaded 归 5xx)。
func (a *Anthropic) Classify(status int, _ []byte) failover.ErrorClass {
	switch {
	case status == http.StatusBadRequest:
		return failover.ClassBadRequest
	case status == http.StatusUnauthorized, status == http.StatusForbidden:
		return failover.ClassAuthFailed
	case status == http.StatusTooManyRequests:
		return failover.ClassRateLimited
	case status >= 500:
		return failover.ClassUpstream5xx
	default:
		return failover.ClassUnknown
	}
}

// NormalizeUsage Anthropic usage → 统一 Usage;同时兼容已是统一形态的输入
// (流式翻译帧自身即统一形态,取数同缝复用)。
func (a *Anthropic) NormalizeUsage(raw json.RawMessage) (protocol.Usage, error) {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return protocol.Usage{}, fmt.Errorf("bridge: 解析 usage: %w", err)
	}
	if _, ok := probe["input_tokens"]; !ok {
		// 统一形态(基座键名),直读
		var u protocol.Usage
		if err := json.Unmarshal(raw, &u); err != nil {
			return protocol.Usage{}, err
		}
		return u, nil
	}
	var u struct {
		Input      int64 `json:"input_tokens"`
		Output     int64 `json:"output_tokens"`
		CacheRead  int64 `json:"cache_read_input_tokens"`
		CacheCreat int64 `json:"cache_creation_input_tokens"`
	}
	if err := json.Unmarshal(raw, &u); err != nil {
		return protocol.Usage{}, fmt.Errorf("bridge: 解析 anthropic usage: %w", err)
	}
	return protocol.Usage{
		PromptTokens:     u.Input,
		CompletionTokens: u.Output,
		CachedTokens:     u.CacheRead,
		ReasoningTokens:  0, // thinking token 计入 output,Anthropic 无独立字段(矩阵已登记)
	}, nil
}

// StreamEvent Anthropic SSE 事件 → OpenAI chunk 翻译(唯一改写者):
//   - message_start      → usage 帧(prompt_tokens/cache);meta.IsUsage
//   - content_block_start(tool_use) → 首个 tool_calls delta(id/name/块序号);meta.HasContent
//   - content_block_delta(text_delta/thinking_delta/input_json_delta)→ content/reasoning_content/tool_calls 增量
//   - message_delta      → finish_reason + completion_tokens;meta.IsUsage
//   - message_stop       → "[DONE]";meta.IsDone
//   - ping / content_block_start(text) / content_block_stop → 空透传(跳过)
//   - type=error         → 报错(上层归 mid_stream_fail / 断言出口)
//
// 全部翻译保持事件级无状态(Adapter 实例跨请求共享,禁止实例字段);tool_call 的
// id/name 由 content_block_start 单事件携带,arguments 增量按事件自带块序号拼接。
//
// 返回的 passthrough 为 OpenAI 形态 chunk 内容(不含 data: 前缀),由 proxy 负责线上封装。
func (a *Anthropic) StreamEvent(data []byte) ([]byte, adapter.EventMeta, error) {
	if len(strings.TrimSpace(string(data))) == 0 {
		// 空 data / 纯注释事件(SSE 心跳):不产出翻译帧也不报错,否则心跳会触发断流出口
		return nil, adapter.EventMeta{}, nil
	}
	var ev struct {
		Type    string `json:"type"`
		Index   int    `json:"index"`
		Message struct {
			Usage struct {
				Input     int64 `json:"input_tokens"`
				CacheRead int64 `json:"cache_read_input_tokens"`
			} `json:"usage"`
		} `json:"message"`
		ContentBlock struct {
			Type string `json:"type"`
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"content_block"`
		Delta struct {
			Type        string  `json:"type"`
			Text        string  `json:"text"`
			Thinking    string  `json:"thinking"`
			PartialJSON string  `json:"partial_json"`
			StopReason  *string `json:"stop_reason"`
		} `json:"delta"`
		Usage struct {
			OutputTokens int64 `json:"output_tokens"`
		} `json:"usage"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &ev); err != nil {
		return nil, adapter.EventMeta{}, fmt.Errorf("bridge: 解析 anthropic 事件: %w", err)
	}
	if ev.Type == "error" || ev.Error != nil {
		return nil, adapter.EventMeta{}, fmt.Errorf("bridge: anthropic 流内错误事件")
	}

	switch ev.Type {
	case "message_start":
		b := fmt.Sprintf(`{"choices":[],"usage":{"prompt_tokens":%d,"completion_tokens":0,"cached_tokens":%d}}`,
			ev.Message.Usage.Input, ev.Message.Usage.CacheRead)
		return []byte(b), adapter.EventMeta{IsUsage: true}, nil

	case "content_block_start":
		if ev.ContentBlock.Type != "tool_use" {
			return nil, adapter.EventMeta{}, nil // text 等块起始对客户端不可见
		}
		// tool_use 块起始即首个 tool_call delta:必须携带 index/id/name(OpenAI 流式契约)
		b := fmt.Sprintf(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":%d,"id":%s,"type":"function","function":{"name":%s,"arguments":""}}]}}]}`,
			ev.Index, mustJSON(ev.ContentBlock.ID), mustJSON(ev.ContentBlock.Name))
		return []byte(b), adapter.EventMeta{HasContent: true}, nil

	case "content_block_delta":
		switch ev.Delta.Type {
		case "text_delta":
			return chunkBytes(`{"choices":[{"index":0,"delta":{"content":` + mustJSON(ev.Delta.Text) + `}}]}`), adapter.EventMeta{HasContent: ev.Delta.Text != ""}, nil
		case "thinking_delta":
			return chunkBytes(`{"choices":[{"index":0,"delta":{"reasoning_content":` + mustJSON(ev.Delta.Thinking) + `}}]}`), adapter.EventMeta{HasContent: ev.Delta.Thinking != ""}, nil
		case "input_json_delta":
			// arguments 增量挂事件自带块序号:并行工具调用各占一段,不得硬编码 0 互相串扰
			return chunkBytes(fmt.Sprintf(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":%d,"function":{"arguments":`, ev.Index) + mustJSON(ev.Delta.PartialJSON) + `}}]}}]}`), adapter.EventMeta{HasContent: ev.Delta.PartialJSON != ""}, nil
		default:
			return nil, adapter.EventMeta{}, nil
		}

	case "message_delta":
		fr := stopToFinish(pointerVal(ev.Delta.StopReason))
		b := fmt.Sprintf(`{"choices":[{"index":0,"delta":{},"finish_reason":%s}],"usage":{"prompt_tokens":0,"completion_tokens":%d}}`,
			mustJSON(fr), ev.Usage.OutputTokens)
		return []byte(b), adapter.EventMeta{IsUsage: true}, nil

	case "message_stop":
		return []byte("[DONE]"), adapter.EventMeta{IsDone: true}, nil

	default: // ping / content_block_start / content_block_stop:对客户端不可见
		return nil, adapter.EventMeta{}, nil
	}
}

// RewriteResponse Anthropic 非流式响应 → OpenAI ChatResponse 形态。
func (a *Anthropic) RewriteResponse(body []byte) ([]byte, json.RawMessage, error) {
	var resp struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Content []struct {
			Type     string          `json:"type"`
			Text     string          `json:"text"`
			Thinking string          `json:"thinking"`
			ID       string          `json:"id"`
			Name     string          `json:"name"`
			Input    json.RawMessage `json:"input"`
		} `json:"content"`
		StopReason *string `json:"stop_reason"`
		Usage      struct {
			Input     int64 `json:"input_tokens"`
			Output    int64 `json:"output_tokens"`
			CacheRead int64 `json:"cache_read_input_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, nil, fmt.Errorf("bridge: 解析 anthropic 响应: %w", err)
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
	for _, blk := range resp.Content {
		switch blk.Type {
		case "text":
			content += blk.Text
		case "thinking":
			reasoning.WriteString(blk.Thinking)
		case "tool_use":
			calls = append(calls, toolCall{
				Index: len(calls), ID: blk.ID, Type: "function",
				Function: struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				}{Name: blk.Name, Arguments: string(blk.Input)},
			})
		}
	}

	msg := map[string]any{"role": "assistant", "content": content}
	if reasoning.Len() > 0 {
		msg["reasoning_content"] = reasoning.String()
	}
	if len(calls) > 0 {
		msg["tool_calls"] = calls
	}
	choice := map[string]any{
		"index":         0,
		"message":       msg,
		"finish_reason": stopToFinish(pointerVal(resp.StopReason)),
	}
	usage := map[string]any{
		"prompt_tokens":     resp.Usage.Input,
		"completion_tokens": resp.Usage.Output,
		// 统一形态缓存字段 + OpenAI details 形态并存:客户端 SDK 兼容,计量取数同缝
		"cached_tokens":         resp.Usage.CacheRead,
		"prompt_tokens_details": map[string]any{"cached_tokens": resp.Usage.CacheRead},
	}
	out := map[string]any{
		// id/model 源自上游原样透出(客户端 SDK 按其做日志关联/去重,恒空串属翻译残缺)
		"id":      resp.ID,
		"model":   resp.Model,
		"object":  "chat.completion",
		"choices": []any{choice},
		"usage":   usage,
	}
	outBytes, err := json.Marshal(out)
	if err != nil {
		return nil, nil, err
	}
	usageBytes, _ := json.Marshal(usage)
	return outBytes, usageBytes, nil
}

// stopToFinish Anthropic stop_reason → OpenAI finish_reason。
// pause_turn/refusal/model_context_window_exceeded 有明确对应语义的显式映射;
// 其余未知新值兜底 "stop"(保守形态,翻译矩阵已登记该取舍)。
func stopToFinish(stop string) string {
	switch stop {
	case "end_turn", "stop_sequence", "pause_turn":
		return "stop"
	case "max_tokens", "model_context_window_exceeded":
		return "length"
	case "tool_use":
		return "tool_calls"
	case "refusal":
		return "content_filter"
	case "":
		return ""
	default:
		return "stop"
	}
}

// contentText 消息内容抽取:字符串原样;多模态数组取 text 部分拼接(非文本块丢弃,见 README 限制)。
func contentText(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, nil
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err == nil {
		var sb strings.Builder
		for _, p := range parts {
			if p.Type == "text" {
				sb.WriteString(p.Text)
			}
		}
		return sb.String(), nil
	}
	return "", fmt.Errorf("无法识别的 content 形态")
}

// chunkBytes 为空内容返回 nil(调用方跳过写出)。
func chunkBytes(payload string) []byte {
	if payload == "" {
		return nil
	}
	return []byte(payload)
}

// mustJSON 极小封装:标准库序列化失败时返回 null(构造的都是可控形态,失败即程序错误)。
func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "null"
	}
	return string(b)
}

// pointerVal 安全取值。
func pointerVal(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// BridgeKind 桥接标记(adapter.Bridger):proxy 对工具多轮流量做适配性豁免的判定依据。
func (a *Anthropic) BridgeKind() string { return "anthropic" }

// ToolRoundtripComplete 声明翻正(anthropic-bridge-tool-roundtrip):工具多轮请求侧翻译
// 完整(tool_calls→tool_use 块/role=tool→合并 tool_result/配对校验),不再落入豁免语义。
func (a *Anthropic) ToolRoundtripComplete() bool { return true }

// ---- 同构直通能力(adapter.NativePreparer;protocol-native-passthrough) ----

// NativeProto 桥接原生协议 = Anthropic Messages(messages 入站 × anthropic 候选直通)。
func (a *Anthropic) NativeProto() protocol.InboundProtocol { return protocol.InboundMessages }

// PrepareNative Anthropic Messages 原始体直通出站:URL/鉴权头/版本头按既有声明,
// body 仅 model 字段原位手术(RewriteModelInPlace),其余逐字节保留——零翻译零字段丢失。
func (a *Anthropic) PrepareNative(in *protocol.NativeRequest, upstreamModel, apiKey string) (*adapter.Outbound, error) {
	if in.Proto != protocol.InboundMessages {
		return nil, fmt.Errorf("bridge: anthropic 直通仅服务 messages 入站,得到 %q", in.Proto)
	}
	body, err := protocol.RewriteModelInPlace(in.Raw, upstreamModel)
	if err != nil {
		return nil, fmt.Errorf("bridge: 直通 model 改写: %w", err)
	}
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	if a.bearer {
		h.Set("Authorization", "Bearer "+apiKey)
	} else {
		h.Set("x-api-key", apiKey)
	}
	h.Set("anthropic-version", anthropicVersion)
	return &adapter.Outbound{URL: a.baseURL + "/v1/messages", Header: h, Body: body}, nil
}

// NativeStreamEvent meta-only 读侧:复用既有事件机的 meta 与翻译字节(翻译字节仅作
// 内部计量/拼包消费,客户端恒收上游原字节——passthrough 由 proxy 执行,此处不改写 wire)。
func (a *Anthropic) NativeStreamEvent(data []byte) ([]byte, adapter.EventMeta, error) {
	return a.StreamEvent(data)
}

// NormalizeNativeUsage 与翻译路径同缝复用(anthropic 形态与统一形态双兼容)。
func (a *Anthropic) NormalizeNativeUsage(raw json.RawMessage) (protocol.Usage, error) {
	return a.NormalizeUsage(raw)
}
