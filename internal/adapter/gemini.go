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

// geminiEnvMsg 是 buildRequest 从 canonical Raw 读取的单条消息：Message 结构不携带
// tool_calls/tool_call_id，与 anthropic 桥接一致。
type geminiEnvMsg struct {
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
}

type geminiEnv struct {
	Messages []geminiEnvMsg `json:"messages"`
}

// buildRequest 将规范 chat 请求转为 Gemini 请求体，包括 system、functionCall 和 functionResponse。
func (g *Gemini) buildRequest(in *protocol.ChatRequest) ([]byte, error) {
	var env geminiEnv
	if err := json.Unmarshal(in.Raw, &env); err != nil {
		return nil, fmt.Errorf("canonical 消息解析失败: %w", err)
	}

	out := &geminiWire{}
	// top_p 在 ChatRequest 无类型化字段，从 Raw 层读取；anthropic 桥接同款处理
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(in.Raw, &fields)
	// tool_call_id 到 name 的回查表，依赖 assistant.tool_calls 先于 role=tool 到达
	callName := map[string]string{}

	if err := g.buildContents(out, env.Messages, callName); err != nil {
		return nil, err
	}
	if len(out.Contents) == 0 {
		return nil, fmt.Errorf("messages 为空或全部不可翻译")
	}

	g.buildSampling(out, in, fields)
	if err := g.buildToolsAndChoice(out, in.Tools, fields); err != nil {
		return nil, err
	}
	return json.Marshal(out)
}

// buildToolsAndChoice 追加 tools 与 toolConfig；两者分别来自 typed 字段与 Raw 层。
func (g *Gemini) buildToolsAndChoice(out *geminiWire, tools json.RawMessage, fields map[string]json.RawMessage) error {
	if tools != nil && string(tools) != "null" {
		decls, err := g.buildTools(tools)
		if err != nil {
			return err
		}
		out.Tools = decls
	}
	if raw, ok := fields["tool_choice"]; ok && len(raw) > 0 && string(raw) != "null" {
		if tc := g.toolChoiceOf(raw); tc != nil {
			out.ToolConfig = map[string]any{"functionCallingConfig": tc}
		}
	}
	return nil
}

// buildContents 逐条消息累积 contents 与 systemInstruction，同时维护 tool_call_id → name 回查表。
func (g *Gemini) buildContents(
	out *geminiWire, msgs []geminiEnvMsg, callName map[string]string,
) error {
	for i := range msgs {
		m := &msgs[i]
		if m.Role == "system" || m.Role == "developer" {
			appendSystemInstruction(out, m.Content)
			continue
		}
		c, err := g.roleContent(m, callName)
		if err != nil {
			return err
		}
		if c != nil {
			out.Contents = append(out.Contents, *c)
		}
	}
	return nil
}

// roleContent 非 system 角色消息 → contents 条目；user/assistant 无可翻译内容时返回 nil。
func (g *Gemini) roleContent(m *geminiEnvMsg, callName map[string]string) (*geminiWireContent, error) {
	switch m.Role {
	case "user":
		return g.userContent(m.Content)
	case "assistant":
		c, err := assistantContent(m, callName)
		if err != nil || len(c.Parts) == 0 {
			return nil, err
		}
		return &c, nil
	case "tool":
		c, err := toolContent(m, callName)
		if err != nil {
			return nil, err
		}
		return &c, nil
	default:
		return nil, nil // 未知角色不在桥接范围，丢弃；anthropic 桥接同款纪律
	}
}

// appendSystemInstruction system/developer 消息 → systemInstruction 的 text 部件，
// 空文本不产出部件。
func appendSystemInstruction(out *geminiWire, content json.RawMessage) {
	text := plainText(content)
	if text == "" {
		return
	}
	if out.SystemInstruction == nil {
		out.SystemInstruction = &geminiWireContent{Parts: []map[string]any{}}
	}
	out.SystemInstruction.Parts = append(out.SystemInstruction.Parts, map[string]any{"text": text})
}

// assistantContent assistant 消息 → role:model 内容：文本在前、functionCall 在后，
// 并按 tool_call id 登记函数名供后续 role=tool 回查。
func assistantContent(m *geminiEnvMsg, callName map[string]string) (geminiWireContent, error) {
	c := geminiWireContent{Role: "model", Parts: []map[string]any{}}
	if text := plainText(m.Content); text != "" {
		c.Parts = append(c.Parts, map[string]any{"text": text})
	}
	for _, tc := range m.ToolCalls {
		callName[tc.ID] = tc.Function.Name
		args, err := jsonDecodeToAny(tc.Function.Arguments)
		if err != nil {
			return c, fmt.Errorf("tool_calls[%s].arguments 非法 JSON: %w", tc.ID, err)
		}
		c.Parts = append(c.Parts, map[string]any{"functionCall": map[string]any{"name": tc.Function.Name, "args": args}})
	}
	return c, nil
}

// toolContent role=tool 消息 → role:user 的 functionResponse 内容，name 由配对的 tool_calls 回查。
func toolContent(m *geminiEnvMsg, callName map[string]string) (geminiWireContent, error) {
	name, ok := callName[m.ToolCallID]
	if !ok {
		return geminiWireContent{}, fmt.Errorf("role=tool 消息找不到配对的 tool_calls(id=%s)", m.ToolCallID)
	}
	response, err := jsonDecodeToAny(toolContentJSON(m.Content))
	if err != nil {
		return geminiWireContent{}, fmt.Errorf("role=tool 消息 content 非法 JSON: %w", err)
	}
	return geminiWireContent{Role: "user", Parts: []map[string]any{
		{"functionResponse": map[string]any{"name": name, "response": response}},
	}}, nil
}

// buildSampling 采样参数：max_tokens 优先，top_p/top_k 从 Raw 层读取后透传。
func (g *Gemini) buildSampling(out *geminiWire, in *protocol.ChatRequest, fields map[string]json.RawMessage) {
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
}

// oaiFunctionTool 是 canonical tools 数组的元素形态。
type oaiFunctionTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

// buildTools canonical tools → functionDeclarations 包裹；非 function 或缺名的条目静默跳过。
func (g *Gemini) buildTools(raw json.RawMessage) ([]map[string]any, error) {
	var oaiTools []oaiFunctionTool
	// 解析失败必须报错归 bad_request；静默吞掉会让上游在无工具状态下作答且无痕迹
	if err := json.Unmarshal(raw, &oaiTools); err != nil {
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
	if len(decls) == 0 {
		return nil, nil
	}
	return []map[string]any{{"functionDeclarations": decls}}, nil
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

// userPart 是 user 消息 content 数组的元素形态。
type userPart struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	ImageURL struct {
		URL string `json:"url"`
	} `json:"image_url"`
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
	var parts []userPart
	if err := json.Unmarshal(raw, &parts); err != nil {
		return nil, fmt.Errorf("user content 形态非法: %w", err)
	}
	out := &geminiWireContent{Role: "user", Parts: []map[string]any{}}
	for _, p := range parts {
		part, err := userPartToGemini(p)
		if err != nil {
			return nil, err
		}
		if part != nil {
			out.Parts = append(out.Parts, part)
		}
	}
	if len(out.Parts) == 0 {
		return nil, nil
	}
	return out, nil
}

// userPartToGemini 单部件映射：文本优先，其后图片（data URL → inlineData，其余 → fileData）；
// 两者皆空返回 nil。
func userPartToGemini(p userPart) (map[string]any, error) {
	switch {
	case p.Text != "":
		return map[string]any{"text": p.Text}, nil
	case p.ImageURL.URL != "":
		return imageURLToGeminiPart(p.ImageURL.URL)
	default:
		return nil, nil
	}
}

// imageURLToGeminiPart data URL 解析为 inlineData，非 data URL 视为 http(s) 文件引用。
func imageURLToGeminiPart(url string) (map[string]any, error) {
	const dataPrefix = "data:"
	if !strings.HasPrefix(url, dataPrefix) {
		// http(s) URL 映射 fileData{fileUri}，文件拉取由上游负责
		return map[string]any{"fileData": map[string]any{"fileUri": url}}, nil
	}
	mime, b64, ok := parseDataURL(url)
	if !ok {
		return nil, fmt.Errorf("image_url data URL 形态非法: %q", truncateURL(url))
	}
	return map[string]any{"inlineData": map[string]any{"mimeType": mime, "data": b64}}, nil
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

// geminiStreamPart 是流帧 candidates[].content.parts[] 的元素形态。
type geminiStreamPart struct {
	Text         string                        `json:"text"`
	Thought      bool                          `json:"thought"`
	FunctionCall *geminiStreamPartFunctionCall `json:"functionCall"`
}

type geminiStreamPartFunctionCall struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

// geminiStreamFrame 是流帧的最小解析形态。
type geminiStreamFrame struct {
	Candidates []struct {
		Content struct {
			Parts []geminiStreamPart `json:"parts"`
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
	var frame geminiStreamFrame
	if err := json.Unmarshal(data, &frame); err != nil {
		return nil, EventMeta{}, fmt.Errorf("gemini: 解析流帧: %w", err)
	}
	if frame.Error != nil {
		return nil, EventMeta{}, fmt.Errorf("gemini: 流内错误帧 status=%s", frame.Error.Status)
	}
	agg := &geminiFrameAgg{callSeq: callSeq, toolIdx: toolIdx}
	for _, c := range frame.Candidates {
		agg.addParts(c.Content.Parts)
		if c.FinishReason != "" {
			agg.finish = c.FinishReason
			// usageMetadata 是上游原生键，不进 chunk，仅在终帧转统一形态
			agg.usagePart = geminiUsageFragment(&frame.UsageMetadata)
		}
	}
	if payload := agg.payload(); payload != nil {
		return payload, EventMeta{HasContent: agg.hasContent, IsUsage: frame.UsageMetadata != nil}, nil
	}
	// 纯 usage 帧或空 candidates：usage 单独成帧供计量，统一形态键名
	if usage := geminiUsageFragment(&frame.UsageMetadata); usage != "" {
		return []byte(`{"choices":[]` + usage + `}`), EventMeta{IsUsage: true}, nil
	}
	return nil, EventMeta{}, nil
}

// geminiFrameAgg 单帧聚合态：文本、思考、工具调用与终帧要素累积后一次性成帧。
type geminiFrameAgg struct {
	callSeq    map[string]int
	toolIdx    *int
	frameCalls int // 帧内 functionCall 序，无状态形态的局部计数
	reasoning  strings.Builder
	content    strings.Builder
	toolDeltas []string
	hasContent bool
	finish     string
	usagePart  string // 形如 ,"usage":{...}，空串表示不带 usage
}

func (a *geminiFrameAgg) addParts(parts []geminiStreamPart) {
	for _, p := range parts {
		switch {
		case p.Text != "" && p.Thought:
			a.reasoning.WriteString(p.Text)
			a.hasContent = true
		case p.Text != "":
			a.content.WriteString(p.Text)
			a.hasContent = true
		case p.FunctionCall != nil:
			a.toolDeltas = append(a.toolDeltas, a.toolCallDelta(p.FunctionCall))
			a.hasContent = true
		}
	}
}

// toolCallDelta 单个 functionCall → tool_calls 增量字面量。
// index/id 合成：每流测序按流计数并同名去重；无状态按帧内序，id 维持既有约定。
func (a *geminiFrameAgg) toolCallDelta(fc *geminiStreamPartFunctionCall) string {
	args := string(fc.Args)
	if args == "" || args == "null" {
		args = "{}"
	}
	idx, id := a.frameCalls, "call_"+fc.Name
	if a.callSeq != nil {
		a.callSeq[fc.Name]++
		if n := a.callSeq[fc.Name]; n > 1 {
			id = fmt.Sprintf("%s_%d", id, n)
		}
		idx = *a.toolIdx
		*a.toolIdx++
	}
	a.frameCalls++
	// id 整体经 JSON 编码：名字含引号或反斜杠时裸拼接会产出非法 JSON 帧
	return `{"index":` + itoa64(int64(idx)) + `,"id":` + mustJSONStrGemini(id) +
		`,"type":"function","function":{"name":` + mustJSONStrGemini(fc.Name) +
		`,"arguments":` + mustJSONStrGemini(args) + `}}`
}

// payload 组装本帧 chunk；无内容也无 finish 时返回 nil，交由调用方判定 usage 帧。
func (a *geminiFrameAgg) payload() []byte {
	if a.reasoning.Len() == 0 && a.content.Len() == 0 && len(a.toolDeltas) == 0 {
		if a.finish == "" {
			return nil
		}
		return []byte(`{"choices":[{"index":0,"delta":{},"finish_reason":` +
			mustJSONStrGemini(geminiFinishToChat(a.finish)) + `}]` + a.usagePart + `}`)
	}
	var fields []string
	if a.reasoning.Len() > 0 {
		fields = append(fields, `"reasoning_content":`+mustJSONStrGemini(a.reasoning.String()))
	}
	if a.content.Len() > 0 {
		fields = append(fields, `"content":`+mustJSONStrGemini(a.content.String()))
	}
	if len(a.toolDeltas) > 0 {
		fields = append(fields, `"tool_calls":[`+strings.Join(a.toolDeltas, ",")+`]`)
	}
	if a.finish == "" {
		return []byte(`{"choices":[{"index":0,"delta":{` + strings.Join(fields, ",") + `}}]}`)
	}
	return []byte(`{"choices":[{"index":0,"delta":{` + strings.Join(fields, ",") +
		`},"finish_reason":` + mustJSONStrGemini(geminiFinishToChat(a.finish)) + `}]` + a.usagePart + `}`)
}

// geminiUsageFragment usageMetadata → 统一形态键名的 usage 片段，前缀逗号以便直接拼接；
// 空指针返回空串。
func geminiUsageFragment(m **geminiUsageMetadata) string {
	if m == nil || *m == nil {
		return ""
	}
	u := *m
	return `,"usage":{"prompt_tokens":` + itoa64(u.PromptTokenCount) +
		`,"completion_tokens":` + itoa64(u.CandidatesTokenCount) +
		`,"cached_tokens":` + itoa64(u.CachedContentTokenCount) +
		`,"reasoning_tokens":` + itoa64(u.ThoughtsTokenCount) + `}`
}

// finishToChat 将 Gemini finishReason 映射为 chat finish_reason；未知值兜底 stop。
func (g *Gemini) finishToChat(f string) string { return geminiFinishToChat(f) }

// geminiFinishToChat 包级映射，供帧聚合与适配器方法共用。
func geminiFinishToChat(f string) string {
	switch f {
	case "MAX_TOKENS":
		return "length"
	case "SAFETY", "RECITATION", "PROHIBITED_CONTENT", "BLOCKLIST", "SPII":
		return "content_filter"
	default: // STOP 及未知新值，含工具调用收束
		return "stop"
	}
}

// geminiResponse 是 GenerateContentResponse 的解析形态，流式与非流式共用。
type geminiResponse struct {
	Candidates []struct {
		Content struct {
			Parts []geminiResponsePart `json:"parts"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
	PromptFeedback *struct {
		BlockReason string `json:"blockReason"`
	} `json:"promptFeedback"`
	UsageMetadata *geminiUsageMetadata `json:"usageMetadata"`
	ModelVersion  string               `json:"modelVersion"`
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

// newChatToolCall 构造 type=function 的完整工具调用对象。
func newChatToolCall(index int, id, name, arguments string) chatToolCall {
	tc := chatToolCall{Index: index, ID: id, Type: "function"}
	tc.Function.Name = name
	tc.Function.Arguments = arguments
	return tc
}

// geminiResponsePart 是响应 candidates[].content.parts[] 的元素形态。
type geminiResponsePart struct {
	Text         string                          `json:"text"`
	Thought      bool                            `json:"thought"`
	FunctionCall *geminiResponsePartFunctionCall `json:"functionCall"`
}

type geminiResponsePartFunctionCall struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

// newGeminiToolCall functionCall → chat tool_calls 元素；同名并行调用按序编号，
// arguments 空或 null 归为 {}。
func newGeminiToolCall(index int, fc *geminiResponsePartFunctionCall, callSeq map[string]int) chatToolCall {
	args := string(fc.Args)
	if args == "" || args == "null" {
		args = "{}"
	}
	callSeq[fc.Name]++
	id := "call_" + fc.Name
	if n := callSeq[fc.Name]; n > 1 {
		id = fmt.Sprintf("%s_%d", id, n)
	}
	return newChatToolCall(index, id, fc.Name, args)
}

// RewriteResponse 将非流式 GenerateContentResponse 转为 OpenAI ChatResponse。
// promptFeedback.blockReason 非空时报错归 content_filter，禁止以空内容伪造成功。
func (g *Gemini) RewriteResponse(body []byte) ([]byte, json.RawMessage, error) {
	var resp geminiResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, nil, fmt.Errorf("gemini: 解析响应: %w", err)
	}
	if resp.PromptFeedback != nil && resp.PromptFeedback.BlockReason != "" {
		return nil, nil, fmt.Errorf("gemini: prompt 被安全策略拦截(blockReason=%s)", resp.PromptFeedback.BlockReason)
	}
	content := ""
	var reasoning strings.Builder
	var calls []chatToolCall
	callSeq := map[string]int{} // 同名并行调用 id 去重，与流式测序和入站翻译同规
	finish := ""
	for i, c := range resp.Candidates {
		if i == 0 {
			finish = g.finishToChat(c.FinishReason)
		}
		g.collectParts(c.Content.Parts, &content, &reasoning, &calls, callSeq)
	}
	msg := map[string]any{"role": "assistant", "content": content}
	if reasoning.Len() > 0 {
		msg["reasoning_content"] = reasoning.String()
	}
	if len(calls) > 0 {
		msg["tool_calls"] = calls
	}
	usage := geminiChatUsage(resp.UsageMetadata)
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

// collectParts 累积候选部件：thought → reasoning，text → content，functionCall → tool_calls。
func (g *Gemini) collectParts(
	parts []geminiResponsePart, content *string, reasoning *strings.Builder,
	calls *[]chatToolCall, callSeq map[string]int,
) {
	for _, p := range parts {
		switch {
		case p.Text != "" && p.Thought:
			reasoning.WriteString(p.Text)
		case p.Text != "":
			*content += p.Text
		case p.FunctionCall != nil:
			*calls = append(*calls, newGeminiToolCall(len(*calls), p.FunctionCall, callSeq))
		}
	}
}

// geminiChatUsage usageMetadata → chat usage 对象；上游缺省时为空对象（保留既有 wire 形态）。
func geminiChatUsage(m *geminiUsageMetadata) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return map[string]any{
		"prompt_tokens":             m.PromptTokenCount,
		"completion_tokens":         m.CandidatesTokenCount,
		"cached_tokens":             m.CachedContentTokenCount,
		"prompt_tokens_details":     map[string]any{"cached_tokens": m.CachedContentTokenCount},
		"completion_tokens_details": map[string]any{"reasoning_tokens": m.ThoughtsTokenCount},
	}
}

// BridgeKind 标记 Gemini 为真异构协议桥接，翻译为 OpenAI 形态。
func (g *Gemini) BridgeKind() string { return "gemini" }

// NativeProto 返回 Gemini，即 gemini 入站对该候选直通。
func (g *Gemini) NativeProto() protocol.InboundProtocol { return protocol.InboundGemini }

// PrepareNative 构造原生 generateContent 直通出站：模型名在 URL 方法后缀，请求体逐字节保留；
// 流式形态由 Stream 标志决定（Gemini wire 的流式语义不在请求体内）。
func (g *Gemini) PrepareNative(in *protocol.NativeRequest, upstreamModel, apiKey string) (*Outbound, error) {
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
		Body:   in.Raw,
	}, nil
}

// NativeStreamEvent 直通读侧：复用无状态帧翻译产出 canonical 内部帧供计量与首 token 判定，
// 客户端收到的仍是上游原始帧。工具跨帧序号只影响内部帧，计量不依赖。
func (g *Gemini) NativeStreamEvent(data []byte) ([]byte, EventMeta, error) {
	return g.StreamEvent(data)
}

// NormalizeNativeUsage 归一上游原生 usageMetadata。
func (g *Gemini) NormalizeNativeUsage(raw json.RawMessage) (protocol.Usage, error) {
	return g.NormalizeUsage(raw)
}

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
