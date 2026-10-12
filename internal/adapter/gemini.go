// Gemini 原生出站适配器(gemini-native-protocol;ADR-0003 真异构协议整家实现):
// 规范 chat → Gemini REST v1beta wire(:generateContent / :streamGenerateContent?alt=sse),
// 响应/流/usage 归一回 OpenAI 形态。工具多轮请求侧翻译完整(functionCall/functionResponse
// 双向),tool_choice 完整映射含 NONE——anthropic 桥接的两处登记缺口在此不存在。
// 映射表见 upstream-adapters.md Gemini 节;fixture=conformance/gemini/。
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

// Gemini 适配器实例(基址绑定;key 由 keypool 每请求提供)。
type Gemini struct {
	baseURL string
}

// NewGemini 构造 Gemini 适配器。
func NewGemini(baseURL string) *Gemini {
	return &Gemini{baseURL: strings.TrimRight(baseURL, "/")}
}

// Prepare 组装出站请求:URL = {base}/models/{上游模型}:generateContent(base_url 含版本段
// ——如官方 https://generativelanguage.googleapis.com/v1beta,与 openai 系「base_url 含 /v1」
// 同款惯例;方法段 :generateContent / :streamGenerateContent?alt=sse 由适配器拼接);
// body 经 buildGeminiRequest 翻译;鉴权 x-goog-api-key(官方支持形态中唯一不把 key
// 暴露进 URL/日志的一种)。
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

// geminiWire 请求体形态(字段顺序由 map 序列化排序,契约以 fixture 字段断言)。
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

// buildRequest canonical chat → Gemini 请求体。消息遍历走 Raw 层完整信封
// (Message 结构不携带 tool_calls/tool_call_id——anthropic 桥接缺口的根因,gemini
// 要完整翻译就必须解析原字段):system → systemInstruction 提升;tool 消息 → role=user
// 的 functionResponse 部件(name 由前置 assistant.tool_calls 回查——Gemini 以 name 配对);
// assistant tool_calls → functionCall 部件(args 反序列化为对象形态)。
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
	// top_p:ChatRequest 无 typed 字段,Raw 层读取(与 anthropic 桥接同缝)
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(in.Raw, &fields)
	// tool_call_id → name 回查表(assistant.tool_calls 先于 role=tool 到达)
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
			// Gemini functionResponse 归 role=user 内容;name 由配对的 tool_calls 回查
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
			continue // 未知角色不在桥接范围,丢弃(与 anthropic 桥接同款纪律)
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
	if in.Tools != nil && string(in.Tools) != "null" {
		var oaiTools []struct {
			Type     string `json:"type"`
			Function struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				Parameters  json.RawMessage `json:"parameters"`
			} `json:"function"`
		}
		// 解析失败必须报错(归 bad_request):静默吞掉会让上游在无工具状态下作答且无痕迹
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

// gencNotNil 惰性初始化 generationConfig(仅存在非缺省参数时才出现在 wire)。
func gencNotNil(m *map[string]any) map[string]any {
	if *m == nil {
		*m = map[string]any{}
	}
	return *m
}

// toolChoiceOf chat tool_choice → functionCallingConfig(完整映射含 NONE):
// auto→AUTO / required→ANY / 具名→ANY+allowedFunctionNames / none→NONE(anthropic 桥接
// 的同名登记缺口在此不存在,design 决策 5)。
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

// userContent user 消息 → content(文本/多模态 data URL → inlineData;http(s) URL → fileData)。
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
				// http(s) URL → fileData{fileUri}(Gemini 文件引用形态;拉取由上游负责)
				out.Parts = append(out.Parts, map[string]any{"fileData": map[string]any{"fileUri": p.ImageURL.URL}})
			}
		}
	}
	if len(out.Parts) == 0 {
		return nil, nil
	}
	return out, nil
}

// parseDataURL data:mime;base64,payload → (mime, payload, true)。
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

// jsonDecodeToAny 原始 JSON → any(对象形态;空/ null → 空对象)。
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

// toolContentJSON role=tool 消息 content → JSON 对象字面量(字符串包装为对象形态,
// 与 Gemini functionResponse.response 的对象语义对齐)。
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

// plainText 消息 content 文本抽取(字符串原样;结构体取 text 部件拼接)。
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

// Classify 按状态码与 body status 字面量归类(failover 判定表 Gemini 格):
// 400/INVALID_ARGUMENT→bad_request;401/403→auth;429/RESOURCE_EXHAUSTED→限流;
// 5xx/UNAVAILABLE/DEADLINE_EXCEEDED→server_error;其余 unknown。
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

// geminiUsageMetadata usageMetadata 原生形态(归一表 Gemini 列;thoughts 归 reasoning 口径,
// 登记:reasoning 为 completion 子集语义,不与 candidates 相加)。
type geminiUsageMetadata struct {
	PromptTokenCount        int64 `json:"promptTokenCount"`
	CandidatesTokenCount    int64 `json:"candidatesTokenCount"`
	CachedContentTokenCount int64 `json:"cachedContentTokenCount"`
	ThoughtsTokenCount      int64 `json:"thoughtsTokenCount"`
}

// NormalizeUsage usage 归一:双形态兼容(上游原生 usageMetadata 键 / 统一形态键——
// 事件机翻译 chunk 内嵌统一形态,与 anthropic 桥接的双形态先例同缝)。
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

// StreamEvent Gemini SSE 帧 → OpenAI chunk 翻译(唯一改写者):
//   - content.parts[].text / thought → content / reasoning_content 增量;HasContent
//   - content.parts[].functionCall → tool_calls 首 delta(完整 args 单帧到达);HasContent
//   - candidates[].finishReason 帧 → finish_reason + usage;IsUsage
//   - {error:{...}} 帧 → 报错(proxy 归类换道/断言出口)
//   - 流无 [DONE]:流关闭即收尾(StreamCloseDone 契约),usage 多帧非零合并
//
// StreamEvent 无状态帧翻译(接口既有形态):工具调用 index 仅帧内计数(同帧多调用
// 0..n-1),跨帧场景由 NewStreamSequencer 每流测序承担。
func (g *Gemini) StreamEvent(data []byte) ([]byte, EventMeta, error) {
	return g.streamEvent(data, nil, nil)
}

// NewStreamSequencer 每流测序(adapter.StreamSequencer):Gemini wire 无工具调用序号,
// 跨帧并行 functionCall 必须按流计数合成 tool_calls index,同名调用 id 按序去重
// (与入站翻译 callSeq 同规:call_<name>、重复为 call_<name>_N)。
func (g *Gemini) NewStreamSequencer() func([]byte) ([]byte, EventMeta, error) {
	seq := &geminiStreamSeq{g: g, callSeq: map[string]int{}}
	return seq.event
}

type geminiStreamSeq struct {
	g       *Gemini
	toolIdx int            // 已发出的 functionCall 总序(跨帧)
	callSeq map[string]int // 同名调用去重计数
}

func (s *geminiStreamSeq) event(data []byte) ([]byte, EventMeta, error) {
	return s.g.streamEvent(data, &s.toolIdx, s.callSeq)
}

// streamEvent 帧翻译核心:toolIdx/callSeq 非 nil = 每流测序(跨帧连续计数+同名去重);
// nil = 无状态形态(帧内局部计数,id 维持 call_<name> 既有约定)。
// 单帧聚合为一个 delta 对象(reasoning_content/content/tool_calls 合法并存,finish_reason
// 可与内容同帧)——逐对象裸拼接会产出非法 SSE data 行(一帧一 JSON 是管道与客户端的共同前提)。
func (g *Gemini) streamEvent(data []byte, toolIdx *int, callSeq map[string]int) ([]byte, EventMeta, error) {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || strings.HasPrefix(trimmed, ":") {
		return nil, EventMeta{}, nil // 心跳/注释:不可见
	}
	if trimmed == "[DONE]" {
		return data, EventMeta{IsDone: true}, nil // 兼容性防御:真 [DONE] 到达按既有语义收尾
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
	frameCalls := 0 // 帧内 functionCall 序(无状态形态的局部计数)
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
				// index/id 合成:每流测序按流计数+同名去重;无状态按帧内序,id 维持既有约定
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
				// id 整体经 JSON 编码:名字含引号/反斜杠时裸拼接会产出非法 JSON 帧
				toolDeltas = append(toolDeltas, `{"index":`+itoa64(int64(idx))+`,"id":`+mustJSONStrGemini(id)+
					`,"type":"function","function":{"name":`+mustJSONStrGemini(p.FunctionCall.Name)+
					`,"arguments":`+mustJSONStrGemini(args)+`}}`)
				hasContent = true
			}
		}
		if c.FinishReason != "" {
			finish = c.FinishReason
			if frame.UsageMetadata != nil {
				// 统一形态键名(与 conformance usage 断言同缝;usageMetadata 是上游原生键,不进 chunk)
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
		// 纯 usage 帧/空 candidates:usage 单独成帧供计量(统一形态键名)
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

// finishToChat Gemini finishReason → chat finish_reason(冻结映射表;未知兜底 stop)。
func (g *Gemini) finishToChat(f string) string {
	switch f {
	case "MAX_TOKENS":
		return "length"
	case "SAFETY", "RECITATION", "PROHIBITED_CONTENT", "BLOCKLIST", "SPII":
		return "content_filter"
	default: // STOP 及未知新值(含工具调用收束)
		return "stop"
	}
}

// RewriteResponse 非流式 GenerateContentResponse → OpenAI ChatResponse。
// promptFeedback.blockReason 非空 → 报错归 content_filter(禁止空内容伪造成功,design 决策 5)。
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
	callSeq := map[string]int{} // 同名并行调用 id 去重(与流式测序/入站翻译同规)
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

// BridgeKind 桥接标记(adapter.Bridger):真异构协议,翻译为 OpenAI 形态(唯一改写者族)。
func (g *Gemini) BridgeKind() string { return "gemini" }

// ToolRoundtripComplete 声明式缺口(设计决策 4):工具多轮请求侧翻译完整
// (functionCall/functionResponse 双向),不落入 anthropic 桥接的登记缺口。
func (g *Gemini) ToolRoundtripComplete() bool { return true }

// StreamCloseIsDone Gemini 流无 [DONE] 终止标记:干净关流即正常收尾(等价 IsDone)。
func (g *Gemini) StreamCloseIsDone() bool { return true }

// itoa64 极小整数转字符串(usage chunk 内联构造用)。
func itoa64(n int64) string { return strconv.FormatInt(n, 10) }

// mustJSONStrGemini / mustJSONRawGemini 极小封装(与 bridge 包同款,包内独立)。
func mustJSONStrGemini(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(b)
}
