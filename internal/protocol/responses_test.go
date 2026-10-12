// Responses 入站翻译契约测试(responses-api-inbound;测试先行域:协议适配)。
// 依据 docs/design/proxy-and-streaming.md「Responses 入站翻译」映射表:
// 合法映射逐格、危险字段显式拒绝、非流式对象形态、流式事件顺序不变式、usage 映射。
package protocol

import (
	"embed"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

//go:embed testdata/responses/*.json
var testdataFS embed.FS

// respReq 构造合法基线请求,测试按字段覆盖。
func respReq(t *testing.T, patch string) []byte {
	t.Helper()
	base := `{"model":"alias-a","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],"stream":false}`
	if patch != "" {
		base = base[:len(base)-1] + "," + patch + "}"
	}
	return []byte(base)
}

func TestTranslateResponsesRequest_合法映射(t *testing.T) {
	cases := []struct {
		name  string
		patch string
		want  string // 期望翻译后 chat JSON 的 messages/tools 片段(子串断言)
	}{
		{"instructions置前", `"instructions":"be nice"`, `"role":"system","content":"be nice"`},
		{"input字符串快捷形态", `"input":"hello"`, `"content":"hello"`},
		{"assistant消息透传", `"input":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]`, `"role":"assistant"`},
		{"多文本part按序拼接", `"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"a"},{"type":"input_text","text":"b"}]}]`, `"content":"ab"`},
		{"function_call映射tool_calls", `"input":[{"type":"function_call","call_id":"c1","name":"f","arguments":"{}"}]`, `"tool_calls":`},
		{"function_call_output映射tool", `"input":[{"type":"function_call_output","call_id":"c1","output":"r"}]`, `"role":"tool"`},
		{"tools顶层name移入function", `"tools":[{"type":"function","name":"f","description":"d","parameters":{}}]`, `"name":"f"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req, err := TranslateResponsesRequest(respReq(t, c.patch))
			if err != nil {
				t.Fatalf("意外报错: %v", err)
			}
			if !strings.Contains(string(req.Raw), c.want) {
				t.Fatalf("翻译产物缺 %q: %s", c.want, req.Raw)
			}
			if req.Model != "alias-a" {
				t.Fatalf("model 应透传别名: %s", req.Model)
			}
		})
	}
}

// TestTranslateResponsesRequest_官方快捷形态 官方规范内合法形态放行与缺省判定边界
// (fix-responses-input-forms;kongming e2e 实证的两处误拒:省略 type、字符串 content)。
// 对拍断言快捷与显式形态 canonical 逐字节一致——缓存指纹/计量同构的漂移探测器。
func TestTranslateResponsesRequest_官方快捷形态(t *testing.T) {
	shortcut := `{"model":"alias-a","input":[{"role":"user","content":"你好"}],"stream":false}`
	typed := `{"model":"alias-a","input":[{"type":"message","role":"user","content":"你好"}],"stream":false}`
	parts := `{"model":"alias-a","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"你好"}]}],"stream":false}`
	for _, body := range []string{shortcut, typed} {
		req, err := TranslateResponsesRequest([]byte(body))
		if err != nil {
			t.Fatalf("官方快捷形态应放行(%s): %v", body, err)
		}
		if !strings.Contains(string(req.Raw), `"content":"你好"`) {
			t.Fatalf("字符串 content 应折叠为文本消息: %s", req.Raw)
		}
	}
	// 对拍:快捷/显式 type/部件数组三形态 canonical 逐字节一致
	reqS, errS := TranslateResponsesRequest([]byte(shortcut))
	reqT, errT := TranslateResponsesRequest([]byte(typed))
	reqP, errP := TranslateResponsesRequest([]byte(parts))
	if errS != nil || errT != nil || errP != nil {
		t.Fatalf("三形态均应翻译成功: %v %v %v", errS, errT, errP)
	}
	if string(reqS.Raw) != string(reqT.Raw) || string(reqS.Raw) != string(reqP.Raw) {
		t.Fatalf("同语义三形态 canonical 应逐字节一致:\n快捷: %s\n显式: %s\n部件: %s", reqS.Raw, reqT.Raw, reqP.Raw)
	}

	// 缺省判定不越界与非法 content 维持拒绝
	rejects := []struct {
		name string
		body string
		code string
	}{
		{"省略type且无role", `{"model":"alias-a","input":[{}]}`, CodeInputInvalid},
		{"content数字", `{"model":"alias-a","input":[{"role":"user","content":123}]}`, CodeInputInvalid},
		{"content null", `{"model":"alias-a","input":[{"role":"user","content":null}]}`, CodeInputInvalid},
		{"content对象", `{"model":"alias-a","input":[{"role":"user","content":{}}]}`, CodeInputInvalid},
		{"未知item类型维持", `{"model":"alias-a","input":[{"type":"input_image","image_url":"x"}]}`, CodeInputItemUnsupported},
	}
	for _, c := range rejects {
		t.Run(c.name, func(t *testing.T) {
			_, err := TranslateResponsesRequest([]byte(c.body))
			var re *ResponsesUnsupportedError
			if !errors.As(err, &re) || re.Code != c.code {
				t.Fatalf("应拒绝且 code=%s: %v", c.code, err)
			}
		})
	}
}

func TestTranslateResponsesRequest_危险字段与非法输入(t *testing.T) {
	cases := []struct {
		name  string
		patch string
		code  string
	}{
		{"previous_response_id", `"previous_response_id":"resp_x"`, "previous_response_id_unsupported"},
		{"background", `"background":true`, "background_unsupported"},
		{"不支持的input项", `"input":[{"type":"input_image","image_url":"x"}]`, "input_item_unsupported"},
		{"不支持的content part", `"input":[{"type":"message","role":"user","content":[{"type":"input_image","image_url":"x"}]}]`, "input_item_unsupported"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := TranslateResponsesRequest(respReq(t, c.patch))
			if err == nil {
				t.Fatalf("应拒绝: %s", c.name)
			}
			var re *ResponsesUnsupportedError
			if !errors.As(err, &re) || re.Code != c.code {
				t.Fatalf("错误码不符: %v", err)
			}
		})
	}
	// input 缺失
	if _, err := TranslateResponsesRequest([]byte(`{"model":"m"}`)); err == nil {
		t.Fatal("缺失 input 应拒绝")
	}
}

func TestTranslateResponsesRequest_参数透传与stream(t *testing.T) {
	req, err := TranslateResponsesRequest(respReq(t, `"stream":true,"max_output_tokens":64,"temperature":0.2,"top_p":0.9,"tool_choice":"auto","tools":[{"type":"function","name":"f"}]`))
	if err != nil {
		t.Fatal(err)
	}
	if !req.Stream || req.MaxTokens == nil || *req.MaxTokens != 64 || req.Temperature == nil || *req.Temperature != 0.2 {
		t.Fatalf("参数透传不符: %+v", req)
	}
	var chat struct {
		MaxTokens    *int64          `json:"max_tokens"`
		ToolChoice   json.RawMessage `json:"tool_choice"`
		TopP         *float64        `json:"top_p"`
		Stream       bool            `json:"stream"`
		Tools        json.RawMessage `json:"tools"`
		Instructions string          `json:"instructions"`
	}
	if err := json.Unmarshal(req.Raw, &chat); err != nil {
		t.Fatal(err)
	}
	if chat.MaxTokens == nil || *chat.MaxTokens != 64 || !chat.Stream || chat.TopP == nil {
		t.Fatalf("canonical chat 字段不符: %s", req.Raw)
	}
	if strings.Contains(string(req.Raw), "instructions") || strings.Contains(string(req.Raw), "max_output_tokens") {
		t.Fatalf("instructions/max_output_tokens 不得残留于 canonical: %s", req.Raw)
	}
}

func TestResponsesObjectFromChat_非流式映射(t *testing.T) {
	chat := []byte(`{"id":"chatcmpl-1","model":"up-a","choices":[{"index":0,"message":{"role":"assistant","content":"你好"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`)
	out, err := ResponsesObjectFromChat(chat, "resp_req-1")
	if err != nil {
		t.Fatal(err)
	}
	var obj struct {
		ID     string `json:"id"`
		Object string `json:"object"`
		Status string `json:"status"`
		Output []struct {
			Type    string `json:"type"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
		Usage struct {
			InputTokens  int64 `json:"input_tokens"`
			OutputTokens int64 `json:"output_tokens"`
			TotalTokens  int64 `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatal(err)
	}
	if obj.ID != "resp_req-1" || obj.Object != "response" || obj.Status != "completed" {
		t.Fatalf("对象形态不符: %s", out)
	}
	if len(obj.Output) != 1 || obj.Output[0].Type != "message" || obj.Output[0].Content[0].Text != "你好" {
		t.Fatalf("output 映射不符: %s", out)
	}
	if obj.Usage.InputTokens != 10 || obj.Usage.OutputTokens != 5 || obj.Usage.TotalTokens != 15 {
		t.Fatalf("usage 映射不符: %s", out)
	}
}

// 流式事件顺序不变式:created 首帧、completed 终帧、delta 仅出现于两者之间、completed 带 usage。
func TestResponsesStreamEncoder_事件序列不变式(t *testing.T) {
	e := NewResponsesStreamEncoder("resp_r1", "alias-a")
	first := e.Init()
	events := []map[string]any{mustEvent(t, first)}
	chunks := []string{
		`{"choices":[{"index":0,"delta":{"role":"assistant"}}]}`,
		`{"choices":[{"index":0,"delta":{"content":"你"}}]}`,
		`{"choices":[{"index":0,"delta":{"content":"好"}}]}`,
		`{"choices":[{"index":0,"delta":{"content":"!"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":3}}`,
	}
	for _, c := range chunks {
		for _, ev := range e.Feed([]byte(c)) {
			events = append(events, mustEvent(t, ev))
		}
	}
	completed := e.Complete()
	events = append(events, mustEvent(t, completed))

	if typ(events[0]) != "response.created" {
		t.Fatalf("首帧必须 created: %v", typ(events[0]))
	}
	if typ(events[len(events)-1]) != "response.completed" {
		t.Fatalf("终帧必须 completed: %v", typ(events[len(events)-1]))
	}
	deltas := 0
	for _, ev := range events {
		switch typ(ev) {
		case "response.output_text.delta":
			deltas++
		case "response.completed":
			u := ev["response"].(map[string]any)["usage"].(map[string]any)
			if u["input_tokens"].(float64) != 10 || u["output_tokens"].(float64) != 3 {
				t.Fatalf("completed.usage 不符: %v", u)
			}
		}
	}
	if deltas != 3 {
		t.Fatalf("应有 3 个 text delta: %d", deltas)
	}
	if strings.Contains(string(first), `"status":"completed"`) {
		t.Fatalf("created 不应携带 completed 状态: %s", first)
	}
}

func TestResponsesStreamEncoder_函数调用增量(t *testing.T) {
	e := NewResponsesStreamEncoder("resp_r2", "alias-a")
	e.Init()
	evs := e.Feed([]byte(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"f","arguments":"{\"a\":"}}]}}]}`))
	evs = append(evs, e.Feed([]byte(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"b\"}"}}]}}]}`))...)
	kinds := []string{}
	for _, ev := range evs {
		kinds = append(kinds, typ(mustEvent(t, ev)))
	}
	if !contains(kinds, "response.function_call_arguments.delta") {
		t.Fatalf("应有 function_call 增量: %v", kinds)
	}
}

func mustEvent(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("事件非法: %v (%s)", err, raw)
	}
	return m
}

func typ(ev map[string]any) string {
	if s, ok := ev["type"].(string); ok {
		return s
	}
	return ""
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// fixture 消费:翻译样例(testdata)作为可执行资产——非流式映射/流式序列的独立对照源。
func TestResponsesFixtures_翻译样例(t *testing.T) {
	raw, err := testdataFS.ReadFile("testdata/responses/request-translation.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		Inbound json.RawMessage `json:"inbound"`
		Expect  struct {
			CanonicalChat json.RawMessage `json:"canonical_chat"`
		} `json:"expect"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	req, err := TranslateResponsesRequest(fx.Inbound)
	if err != nil {
		t.Fatal(err)
	}
	var got, want map[string]any
	if err := json.Unmarshal(req.Raw, &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(fx.Expect.CanonicalChat, &want); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"messages", "tools", "tool_choice", "max_tokens"} {
		gj, _ := json.Marshal(got[key])
		wj, _ := json.Marshal(want[key])
		if want[key] == nil {
			continue
		}
		if string(gj) != string(wj) {
			t.Fatalf("fixture %s 不符:\n got=%s\nwant=%s", key, gj, wj)
		}
	}
}

func TestTranslateResponsesRequest_toolChoice具名与instructions置前(t *testing.T) {
	// 具名形态(域A 审查:{"type":"function","name"} 的 type 恒存在,旧条件永不命中)
	req, err := TranslateResponsesRequest(respReq(t, `"tool_choice":{"type":"function","name":"f"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(req.Raw), `"tool_choice":{"function":{"name":"f"}`) ||
		strings.Contains(string(req.Raw), `"name":"f","type"`) {
		t.Fatalf("具名 tool_choice 应移入 function: %s", req.Raw)
	}
	// 不支持的 tool_choice 对象形态显式拒绝(非静默透传)
	if _, err := TranslateResponsesRequest(respReq(t, `"tool_choice":{"type":"allowed_tools","mode":"auto"}`)); err == nil {
		t.Fatal("allowed_tools 应显式拒绝")
	}
	// instructions 置前:system 必须是 messages 首元素(顺序断言,非仅存在性)
	req, err = TranslateResponsesRequest(respReq(t, `"instructions":"be nice"`))
	if err != nil {
		t.Fatal(err)
	}
	var chat struct {
		Messages []struct {
			Role string `json:"role"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(req.Raw, &chat); err != nil {
		t.Fatal(err)
	}
	if len(chat.Messages) != 2 || chat.Messages[0].Role != "system" {
		t.Fatalf("instructions 应置前为首个 system: %s", req.Raw)
	}
}

// 并行多 tool calls:按内层 index 独立跟踪,added/args/done 不串号(域B 审查 A 项钉子)。
func TestResponsesStreamEncoder_并行工具项(t *testing.T) {
	e := NewResponsesStreamEncoder("resp_p1", "alias-a")
	e.Init()
	var events []map[string]any
	feed := func(chunk string) {
		for _, ev := range e.Feed([]byte(chunk)) {
			events = append(events, mustEvent(t, ev))
		}
	}
	feed(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"f1","arguments":"{\"x\":"}}]}}]}`)
	feed(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"c2","function":{"name":"f2","arguments":"["}}]}}]}`)
	feed(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"a\"}"}}]}}]}`)
	feed(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"arguments":"1]"}}]}}]}`)
	feed(`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":5,"completion_tokens":9}}`)
	events = append(events, mustEvent(t, e.Complete()))
	doneByCall := map[string]string{}
	addedByCall := map[string]bool{}
	argDelta := 0
	completed := map[string]any{}
	for _, ev := range events {
		switch typ(ev) {
		case "response.output_item.added":
			item := ev["item"].(map[string]any)
			addedByCall[item["call_id"].(string)] = true
		case "response.function_call_arguments.delta":
			argDelta++
		case "response.output_item.done":
			item := ev["item"].(map[string]any)
			doneByCall[item["call_id"].(string)] = item["arguments"].(string)
		case "response.completed":
			completed = ev["response"].(map[string]any)
		}
	}
	if len(doneByCall) != 2 || doneByCall["c1"] != `{"x":"a"}` || doneByCall["c2"] != `[1]` {
		t.Fatalf("并行工具 done 应各自配对: %v", doneByCall)
	}
	if !addedByCall["c1"] || !addedByCall["c2"] {
		t.Fatalf("两个工具都应有 added: %v", addedByCall)
	}
	if argDelta != 4 {
		t.Fatalf("应有 4 个参数增量: %d", argDelta)
	}
	u := completed["usage"].(map[string]any)
	if u["output_tokens"].(float64) != 9 {
		t.Fatalf("completed.usage 不符: %v", u)
	}
}

// TestTranslateResponsesRequest_原始体保留与状态提取 直通双形态(protocol-native-passthrough):
// 失格(input_image 等)降级为失格标记而非 error;previous_response_id 改为提取供粘性路由
// 判定(翻译路径拒绝语义由 legacy wrapper 维持);原始体逐字节保留。
func TestTranslateResponsesRequest_原始体保留与状态提取(t *testing.T) {
	raw := []byte(`{"model":"gpt-x","stream":true,"previous_response_id":"resp_1",` +
		`"input":[{"type":"message","role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,xx"}]}]}`)
	res := TranslateResponsesRequestEx(raw)
	if res.Chat != nil || res.Ineligible == nil {
		t.Fatalf("input_image 部件应失格: %+v", res)
	}
	var ue *ResponsesUnsupportedError
	if !errors.As(res.Ineligible, &ue) || ue.Code != CodeInputItemUnsupported {
		t.Fatalf("失格机器码不符: %v", res.Ineligible)
	}
	if string(res.Raw) != string(raw) {
		t.Fatalf("原始体必须逐字节保留: %s", res.Raw)
	}
	if res.Proto != InboundResponses || res.Model != "gpt-x" || !res.Stream || res.PrevID != "resp_1" {
		t.Fatalf("失格请求的路由/状态信息不符: %+v", res)
	}

	// 状态请求(previous_response_id)在 Ex 不再拒绝:canonical 照算,拒绝判定移交路径决策
	rawPrev := []byte(`{"model":"m","previous_response_id":"resp_9","input":"你好"}`)
	resPrev := TranslateResponsesRequestEx(rawPrev)
	if resPrev.Fatal != nil || resPrev.Ineligible != nil || resPrev.Chat == nil {
		t.Fatalf("状态请求应翻译成功(Ex): %+v", resPrev)
	}
	if resPrev.PrevID != "resp_9" {
		t.Fatalf("PrevID 应提取: %+v", resPrev)
	}
	if !strings.Contains(string(resPrev.Raw), `"previous_response_id":"resp_9"`) {
		t.Fatalf("原始体必须逐字节保留: %s", resPrev.Raw)
	}

	// legacy wrapper(翻译路径契约)维持拒绝清单零改动
	if _, err := TranslateResponsesRequest(raw); err == nil {
		t.Fatal("legacy: 失格应报错")
	}
	_, err := TranslateResponsesRequest(rawPrev)
	var le *ResponsesUnsupportedError
	if !errors.As(err, &le) || le.Code != CodePreviousResponseIDUnsupported {
		t.Fatalf("legacy: previous_response_id 应维持拒绝: %v", err)
	}
	if _, err := TranslateResponsesRequest([]byte(`{"model":"m","background":true,"input":"x"}`)); err == nil {
		t.Fatal("legacy: background 应维持拒绝")
	}
}

// TestChatFromResponsesObject_reasoning摘要 canonical 侧 reasoning 双形态兼容:
// 官方 summary[]（summary_text）与简化实现的顶层 text 皆入 reasoning_content。
func TestChatFromResponsesObject_reasoning摘要(t *testing.T) {
	out, err := ChatFromResponsesObject([]byte(
		`{"id":"resp_1","status":"completed","output":[` +
			`{"type":"reasoning","summary":[{"type":"summary_text","text":"先查"}]},` +
			`{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"reasoning_content":"先查"`) {
		t.Fatalf("官方 summary[] 应入 reasoning_content: %s", out)
	}
	out2, err := ChatFromResponsesObject([]byte(
		`{"id":"resp_1","status":"completed","output":[` +
			`{"type":"reasoning","text":"再想"},` +
			`{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out2), `"reasoning_content":"再想"`) {
		t.Fatalf("顶层 text 简化形态应兼容: %s", out2)
	}
}

// TestChatFromResponsesObject_status保真 canonical 侧 finish_reason 按对象 status 保真:
// incomplete 不得伪造 stop(伪造会让未完成响应经缓存准入流入跨协议回放)。
func TestChatFromResponsesObject_status保真(t *testing.T) {
	completed, err := ChatFromResponsesObject([]byte(
		`{"id":"resp_1","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":3,"output_tokens":2}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(completed), `"finish_reason":"stop"`) {
		t.Fatalf("completed 应映射 stop: %s", completed)
	}
	incomplete, err := ChatFromResponsesObject([]byte(
		`{"id":"resp_1","status":"incomplete","output":[{"type":"message","content":[{"type":"output_text","text":"截断"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(incomplete), `"finish_reason":"length"`) {
		t.Fatalf("incomplete 应映射 length: %s", incomplete)
	}
}
