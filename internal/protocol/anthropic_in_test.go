// Anthropic Messages 入站翻译测试：请求映射、显式拒绝清单、非流式对象映射，
// 流式事件顺序不变式为 message_start 首帧、message_stop 终帧。
package protocol

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// TestTranslateMessages_基础映射 请求侧映射核心格：system 提升、字符串快捷、
// max_tokens/temperature/top_p/stop 透传。
func TestTranslateMessages_基础映射(t *testing.T) {
	body := `{
		"model":"alias-a","max_tokens":1024,"temperature":0.5,"top_p":0.9,
		"system":"你是助手",
		"messages":[{"role":"user","content":"你好"}],
		"stop_sequences":["END"],"stream":false
	}`
	req, err := TranslateMessagesRequest([]byte(body))
	if err != nil {
		t.Fatalf("翻译失败: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(req.Raw, &raw); err != nil {
		t.Fatalf("canonical 非法: %v", err)
	}
	msgs := raw["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("system 提升 + user 应共 2 条: %s", req.Raw)
	}
	if m0 := msgs[0].(map[string]any); m0["role"] != "system" || m0["content"] != "你是助手" {
		t.Fatalf("system 消息不符: %v", m0)
	}
	if raw["max_tokens"].(float64) != 1024 || raw["temperature"].(float64) != 0.5 {
		t.Fatalf("采样参数不符: %s", req.Raw)
	}
	stop := raw["stop"].([]any)
	if len(stop) != 1 || stop[0].(string) != "END" {
		t.Fatalf("stop_sequences 应映射为 stop: %s", req.Raw)
	}
	if raw["stream"] != nil {
		t.Fatalf("stream=false 不得出现在 canonical: %s", req.Raw)
	}
}

// TestTranslateMessages_工具全链 tool 声明(input_schema→parameters)、assistant 的
// tool_use 映射为 arguments 字符串、user tool_result(tool_use_id→tool_call_id)。
func TestTranslateMessages_工具全链(t *testing.T) {
	body := `{
		"model":"alias-a","max_tokens":512,
		"tools":[{"name":"get_weather","description":"查天气","input_schema":{"type":"object","properties":{"city":{"type":"string"}}}}],
		"tool_choice":{"type":"tool","name":"get_weather"},
		"messages":[
			{"role":"user","content":"北京天气?"},
			{"role":"assistant","content":[{"type":"tool_use","id":"tu_1","name":"get_weather","input":{"city":"北京"}}]},
			{"role":"user","content":[
				{"type":"tool_result","tool_use_id":"tu_1","content":[{"type":"text","text":"晴 25 度"}]},
				{"type":"text","text":"谢谢"}
			]}
		]
	}`
	req, err := TranslateMessagesRequest([]byte(body))
	if err != nil {
		t.Fatalf("翻译失败: %v", err)
	}
	var raw map[string]any
	_ = json.Unmarshal(req.Raw, &raw)

	tools := raw["tools"].([]any)
	fn := tools[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "get_weather" || fn["parameters"] == nil {
		t.Fatalf("工具声明映射不符: %s", req.Raw)
	}
	tc := raw["tool_choice"].(map[string]any)
	if tc["type"] != "function" || tc["function"].(map[string]any)["name"] != "get_weather" {
		t.Fatalf("tool_choice 具名映射不符: %s", req.Raw)
	}
	msgs := raw["messages"].([]any)
	if len(msgs) != 4 {
		t.Fatalf("user/assistant(tool)/tool/user 应共 4 条: %s", req.Raw)
	}
	asst := msgs[1].(map[string]any)
	calls := asst["tool_calls"].([]any)
	call0 := calls[0].(map[string]any)
	if call0["id"] != "tu_1" || call0["function"].(map[string]any)["arguments"] != `{"city":"北京"}` {
		t.Fatalf("tool_use → tool_calls 映射不符: %s", req.Raw)
	}
	toolMsg := msgs[2].(map[string]any)
	if toolMsg["role"] != "tool" || toolMsg["tool_call_id"] != "tu_1" || toolMsg["content"] != "晴 25 度" {
		t.Fatalf("tool_result → tool 消息不符: %s", req.Raw)
	}
	if msgs[3].(map[string]any)["content"] != "谢谢" {
		t.Fatalf("tool_result 后的文本块应成为 user 消息: %s", req.Raw)
	}
}

// TestTranslateMessages_拒绝与丢弃清单 image 块/未知块显式拒绝；thinking 块丢弃；
// tool_choice any→required。
func TestTranslateMessages_拒绝与丢弃清单(t *testing.T) {
	cases := []struct {
		name  string
		body  string
		ICode string
	}{
		{"image块拒绝", `{"model":"m","messages":[{"role":"user","content":[{"type":"image","source":{}}]}]}`, CodeContentBlockUnsupported},
		{"未知块拒绝", `{"model":"m","messages":[{"role":"user","content":[{"type":"document"}]}]}`, CodeContentBlockUnsupported},
		{"缺model", `{"messages":[{"role":"user","content":"hi"}]}`, CodeInputInvalid},
		{"空messages", `{"model":"m","messages":[]}`, CodeInputInvalid},
		{"角色非法", `{"model":"m","messages":[{"role":"tool","content":"x"}]}`, CodeInputInvalid},
	}
	for _, c := range cases {
		_, err := TranslateMessagesRequest([]byte(c.body))
		var ue *MessagesUnsupportedError
		if err == nil || !errors.As(err, &ue) || ue.Code != c.ICode {
			t.Fatalf("%s: 应拒绝 %s,实际 %v", c.name, c.ICode, err)
		}
	}
	// thinking 块丢弃 + any→required
	ok, err := TranslateMessagesRequest([]byte(`{"model":"m","tool_choice":{"type":"any"},"messages":[
		{"role":"assistant","content":[{"type":"thinking","thinking":"..."},{"type":"text","text":"答"}]},
		{"role":"user","content":"继续"}]}`))
	if err != nil {
		t.Fatalf("thinking 块应丢弃而非拒绝: %v", err)
	}
	if !strings.Contains(string(ok.Raw), `"tool_choice":"required"`) {
		t.Fatalf("any 应映射 required: %s", ok.Raw)
	}
	if strings.Contains(string(ok.Raw), "thinking") {
		t.Fatalf("thinking 块不得进入 canonical: %s", ok.Raw)
	}
}

// TestMessageFromChat_映射 非流式：reasoning→thinking、text、tool_calls 的 arguments
// 解析为 input 对象、stop_reason 三格、usage(cached)。
func TestMessageFromChat_映射(t *testing.T) {
	chat := `{"model":"up-a","choices":[{"message":{
		"role":"assistant","content":"答案","reasoning_content":"思考过程",
		"tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{\"x\":1}"}}]
	},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"cached_tokens":3}}`
	out, err := MessageFromChat([]byte(chat), "msg_1", "alias-a")
	if err != nil {
		t.Fatalf("改写失败: %v", err)
	}
	var obj struct {
		ID         string  `json:"id"`
		Type       string  `json:"type"`
		Role       string  `json:"role"`
		Model      string  `json:"model"`
		StopReason *string `json:"stop_reason"`
		Content    []struct {
			Type     string          `json:"type"`
			Text     string          `json:"text"`
			Thinking string          `json:"thinking"`
			ID       string          `json:"id"`
			Name     string          `json:"name"`
			Input    json.RawMessage `json:"input"`
		} `json:"content"`
		Usage map[string]int64 `json:"usage"`
	}
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatalf("非 Anthropic 形态: %v (%s)", err, out)
	}
	if obj.ID != "msg_1" || obj.Type != "message" || obj.Role != "assistant" || obj.Model != "up-a" {
		t.Fatalf("头字段不符: %s", out)
	}
	if obj.StopReason == nil || *obj.StopReason != "tool_use" {
		t.Fatalf("tool_calls 应映射 tool_use: %s", out)
	}
	if len(obj.Content) != 3 ||
		obj.Content[0].Type != "thinking" || obj.Content[0].Thinking != "思考过程" ||
		obj.Content[1].Type != "text" || obj.Content[1].Text != "答案" ||
		obj.Content[2].Type != "tool_use" || obj.Content[2].Name != "f" ||
		string(obj.Content[2].Input) != `{"x":1}` {
		t.Fatalf("content 映射不符: %s", out)
	}
	if obj.Usage["input_tokens"] != 10 || obj.Usage["output_tokens"] != 5 || obj.Usage["cache_read_input_tokens"] != 3 {
		t.Fatalf("usage 不符: %s", out)
	}
}

// TestMessagesStreamEncoder_事件序列 流式不变式：message_start 首帧、message_stop 终帧、
// 块切换先 stop 后 start、tool_use 并行块独立、[DONE] 前两连发(message_delta+message_stop)。
func TestMessagesStreamEncoder_事件序列(t *testing.T) {
	enc := NewMessagesStreamEncoder("msg_x", "alias-a")
	var types []string
	feed := func(frames [][]byte) {
		for _, f := range frames {
			var ev struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(f, &ev) == nil {
				types = append(types, ev.Type)
			}
		}
	}
	chunk := func(s string) []byte { return []byte(s) }

	feed(enc.Feed(chunk(`{"choices":[{"index":0,"delta":{"role":"assistant"}}]}`)))
	feed(enc.Feed(chunk(`{"choices":[{"index":0,"delta":{"reasoning_content":"想"}}]}`)))
	feed(enc.Feed(chunk(`{"choices":[{"index":0,"delta":{"content":"你"}}]}`)))
	feed(enc.Feed(chunk(`{"choices":[{"index":0,"delta":{"content":"好"}}]}`)))
	feed(enc.Feed(chunk(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"f","arguments":"{\""}}]}}]}`)))
	feed(enc.Feed(chunk(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"x\":1}"}}]}}]}`)))
	feed(enc.Feed(chunk(`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":7,"completion_tokens":2}}`)))
	feed(enc.Complete())

	if types[0] != "message_start" {
		t.Fatalf("首帧应为 message_start: %v", types)
	}
	if types[len(types)-1] != "message_stop" {
		t.Fatalf("终帧应为 message_stop: %v", types)
	}
	for i, ty := range types {
		if strings.HasSuffix(ty, "_delta") && (i == 0 || i == len(types)-1) {
			t.Fatalf("delta 不得为首/终帧: %v", types)
		}
	}
	// thinking→text→tool_use 切换：每次切换前必须有 content_block_stop
	open := 0
	for _, ty := range types {
		switch ty {
		case "content_block_start":
			if open > 0 {
				t.Fatalf("前一块未收束即开新块: %v", types)
			}
			open++
		case "content_block_stop":
			open--
		}
	}
	if open != 0 {
		t.Fatalf("块开合不配平: %v", types)
	}
	seen := map[string]bool{}
	for _, ty := range types {
		seen[ty] = true
	}
	if !seen["message_delta"] {
		t.Fatalf("缺 message_delta: %v", types)
	}
}

// TestMessagesStreamEncoder_空内容流 无任何内容帧时 [DONE] 仍须保证首帧不变式。
func TestMessagesStreamEncoder_空内容流(t *testing.T) {
	enc := NewMessagesStreamEncoder("msg_y", "alias-a")
	first := enc.Complete()
	var ev struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(first[0], &ev) != nil || ev.Type != "message_start" {
		t.Fatalf("空内容流首事件应为补发的 message_start: %s", first[0])
	}
	last := first[len(first)-1]
	if json.Unmarshal(last, &ev) != nil || ev.Type != "message_stop" {
		t.Fatalf("终事件应为 message_stop: %s", last)
	}
}

// TestTranslateMessages_原始体保留与失格标记 直通双形态：翻译失格如 image 块降级为
// 失格标记而非 error，原始体逐字节保留；可翻译扩展字段 thinking 回传、cache_control、
// stop_sequences、top_k、metadata 翻译成功且原始体零丢失。
func TestTranslateMessages_原始体保留与失格标记(t *testing.T) {
	// 失格：image 块 → 携带机器码的 Ineligible，原始体逐字节保留，路由信息仍可得
	raw := []byte(`{"model":"claude-x","max_tokens":64,"stream":true,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGk="}}]}]}`)
	res := TranslateMessagesRequestEx(raw)
	if res.Chat != nil || res.Ineligible == nil {
		t.Fatalf("image 块应失格而非成功/无标记: %+v", res)
	}
	var ue *MessagesUnsupportedError
	if !errors.As(res.Ineligible, &ue) || ue.Code != CodeContentBlockUnsupported {
		t.Fatalf("失格机器码不符: %v", res.Ineligible)
	}
	if string(res.Raw) != string(raw) {
		t.Fatalf("原始体必须逐字节保留: %s", res.Raw)
	}
	if res.Proto != InboundMessages || res.Model != "claude-x" || !res.Stream {
		t.Fatalf("失格请求的路由信息不符: %+v", res)
	}

	// 可翻译扩展字段：翻译成功且原始体逐字节保留，这是直通保真的协议层前提
	raw2 := []byte(`{"model":"m","max_tokens":32,"top_k":3,"metadata":{"user_id":"u"},"stop_sequences":["END"],` +
		`"messages":[{"role":"user","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral"}}]},` +
		`{"role":"assistant","content":[{"type":"thinking","thinking":"...","signature":"sig"}]},` +
		`{"role":"user","content":"再想"}]}`)
	res2 := TranslateMessagesRequestEx(raw2)
	if res2.Ineligible != nil || res2.Chat == nil {
		t.Fatalf("扩展字段应翻译成功: %+v", res2)
	}
	if string(res2.Raw) != string(raw2) {
		t.Fatalf("可翻译请求的原始体也必须逐字节保留: %s", res2.Raw)
	}
	if res2.Model != "m" || res2.Stream {
		t.Fatalf("路由信息不符: %+v", res2)
	}

	// 结构性非法仍是 Fatal，恒 400
	if r := TranslateMessagesRequestEx([]byte(`{"model":"","messages":[]}`)); r.Fatal == nil || r.Ineligible != nil {
		t.Fatalf("结构性非法应为 Fatal: %+v", r)
	}
}
