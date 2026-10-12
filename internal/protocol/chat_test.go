// protocol 契约测试:入站解析、出站改写(未知字段零丢失)、流式 chunk 首内容判定、
// 错误信封与状态映射。协议适配属测试先行域:本文件先于实现存在(conventions「测试范式」)。
package protocol

import (
	"encoding/json"
	"testing"
)

func TestRewriteModel_未知字段零丢失(t *testing.T) {
	cases := []struct {
		name  string
		body  string
		wantM string // 改写后的 model
	}{
		{
			name:  "常规字段+未知字段共存",
			body:  `{"model":"alias-a","messages":[{"role":"user","content":"hi"}],"temperature":0.5,"vendor_ext":{"foo":1},"top_k":7}`,
			wantM: "upstream-a",
		},
		{
			name:  "仅 messages 与未知顶层字段",
			body:  `{"messages":[],"muniu_future_field":[1,2,3],"model":"alias-b"}`,
			wantM: "upstream-b",
		},
		{
			name:  "model 不存在时补写",
			body:  `{"messages":[{"role":"user","content":"x"}]}`,
			wantM: "upstream-c",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := RewriteModel([]byte(c.body), c.wantM)
			if err != nil {
				t.Fatalf("RewriteModel 意外报错: %v", err)
			}
			var orig, got map[string]json.RawMessage
			if err := json.Unmarshal([]byte(c.body), &orig); err != nil {
				t.Fatalf("原始 body 解析失败: %v", err)
			}
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatalf("改写后 body 解析失败: %v", err)
			}
			// 字段集合等价:改写不得增删字段(model 除外)
			if len(orig) != len(got) && len(orig)+1 != len(got) {
				// 允许"原本无 model、改写后新增"一种情形
				t.Fatalf("字段数不齐: 原始 %d 个, 改写后 %d 个", len(orig), len(got))
			}
			for k, v := range orig {
				if k == "model" {
					continue
				}
				gv, ok := got[k]
				if !ok {
					t.Fatalf("字段 %s 在改写后丢失", k)
				}
				if string(v) != string(gv) {
					t.Fatalf("字段 %s 值被改写: 原始 %s → %s", k, v, gv)
				}
			}
			// model 已替换为目标
			var m struct {
				Model string `json:"model"`
			}
			if err := json.Unmarshal(out, &m); err != nil || m.Model != c.wantM {
				t.Fatalf("model 未改写为 %q: got=%q err=%v", c.wantM, m.Model, err)
			}
		})
	}
}

func TestRewriteModel_非法JSON报错(t *testing.T) {
	if _, err := RewriteModel([]byte(`{not-json`), "x"); err == nil {
		t.Fatal("非法 JSON 应报错,实际无错")
	}
}

func TestParseChatRequest(t *testing.T) {
	body := `{"model":"deepseek-chat","stream":true,"messages":[{"role":"user","content":"你好"}],"temperature":0.3,"max_tokens":512,"unknown_key":true}`
	req, err := ParseChatRequest([]byte(body))
	if err != nil {
		t.Fatalf("合法请求被拒: %v", err)
	}
	if req.Model != "deepseek-chat" || !req.Stream || len(req.Messages) != 1 {
		t.Fatalf("字段解析不符: %+v", req)
	}
	if req.Temperature == nil || *req.Temperature != 0.3 || req.MaxTokens == nil || *req.MaxTokens != 512 {
		t.Fatalf("可选指针字段解析不符: %+v %+v", req.Temperature, req.MaxTokens)
	}
	// 原始字节必须完整持有(生命周期第 7 步契约:适配层转换基于 Raw)
	if string(req.Raw) != body {
		t.Fatalf("Raw 与入站字节不等价")
	}
	if req.Messages[0].Role != "user" {
		t.Fatalf("message role 解析不符: %q", req.Messages[0].Role)
	}
}

func TestParseChatRequest_非法输入(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"空 body", ``},
		{"非 JSON", `hello`},
		{"缺 model", `{"messages":[{"role":"user","content":"x"}]}`},
		{"空 model", `{"model":"","messages":[{"role":"user","content":"x"}]}`},
		{"缺 messages", `{"model":"m"}`},
		{"messages 为对象", `{"model":"m","messages":{"role":"user"}}`},
		{"message 缺 role", `{"model":"m","messages":[{"content":"x"}]}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := ParseChatRequest([]byte(c.body)); err == nil {
				t.Fatalf("期望报错,实际通过: %s", c.body)
			}
		})
	}
}

func TestStreamChunk_首内容判定(t *testing.T) {
	// 首 token 边界(proxy-and-streaming):role 帧/空 delta/usage 终帧不算实质内容
	cases := []struct {
		name    string
		data    string
		hasBody bool // 是否携带实质内容
	}{
		{"role 帧不算", `{"choices":[{"delta":{"role":"assistant"}}]}`, false},
		{"空 delta 不算", `{"choices":[{"delta":{}}]}`, false},
		{"content 帧算", `{"choices":[{"delta":{"content":"你"}}]}`, true},
		{"reasoning_content 帧算", `{"choices":[{"delta":{"reasoning_content":"思考"}}]}`, true},
		{"tool_calls 帧算", `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1"}]}}]}`, true},
		{"usage 终帧不算", `{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1}}`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			chunk, err := ParseStreamChunk([]byte(c.data))
			if err != nil {
				t.Fatalf("合法 chunk 解析失败: %v", err)
			}
			if chunk.HasContent() != c.hasBody {
				t.Fatalf("HasContent()=%v, 期望 %v", chunk.HasContent(), c.hasBody)
			}
			if chunk.IsUsageOnly() != (c.name == "usage 终帧不算") {
				t.Fatalf("IsUsageOnly() 判定不符: %s", c.name)
			}
		})
	}
}

func TestUsage_统一形态(t *testing.T) {
	var u Usage
	raw := `{"prompt_tokens":12,"completion_tokens":34,"reasoning_tokens":5,"cached_tokens":6}`
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		t.Fatalf("usage 解析失败: %v", err)
	}
	if u.PromptTokens != 12 || u.CompletionTokens != 34 || u.ReasoningTokens != 5 || u.CachedTokens != 6 {
		t.Fatalf("usage 字段不符: %+v", u)
	}
}

func TestErrorEnvelope_状态映射(t *testing.T) {
	// conventions 错误信封:HTTP 状态映射表
	cases := map[string]int{
		"bad_request":    400,
		"auth_failed":    401,
		"quota_exceeded": 402,
		"rate_limited":   429,
		"timeout":        504,
		"upstream_error": 502,
		"internal":       500,
		"未知类型兜底":         500,
	}
	for typ, want := range cases {
		if got := StatusForType(typ); got != want {
			t.Fatalf("StatusForType(%q)=%d, 期望 %d", typ, got, want)
		}
	}
}

func TestErrorEnvelope_序列化(t *testing.T) {
	env := NewErrorEnvelope("upstream_error", "all_candidates_failed", "已尝试全部候选", "01JXYZ")
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	var back map[string]any
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("回读失败: %v", err)
	}
	e, ok := back["error"].(map[string]any)
	if !ok {
		t.Fatalf("缺少 error 包装层: %s", b)
	}
	for _, k := range []string{"message", "type", "code", "request_id"} {
		if _, ok := e[k]; !ok {
			t.Fatalf("error.%s 缺失: %s", k, b)
		}
	}
	if _, ok := back["message"]; ok {
		t.Fatal("message 不得裸露在顶层(conventions 信封格式)")
	}
}

// TestRewriteModelInPlace_原位手术 直通保真原语:仅 model 值字节变化,
// 键序/空白/未知字段逐字节保留(RewriteModel 的 map 往返会重排键序,不适用直通)。
func TestRewriteModelInPlace_原位手术(t *testing.T) {
	raw := []byte(`{"zoo":1 ,"model":"alias-a","messages":[{"role":"user","content":"你好"}],"stream":true,  "top_k":  3}`)
	out, err := RewriteModelInPlace(raw, "up-model-x")
	if err != nil {
		t.Fatal(err)
	}
	want := `{"zoo":1 ,"model":"up-model-x","messages":[{"role":"user","content":"你好"}],"stream":true,  "top_k":  3}`
	if string(out) != want {
		t.Fatalf("原位手术破坏了非 model 字节:\n got=%s\nwant=%s", out, want)
	}
	if _, err := RewriteModelInPlace([]byte(`{"messages":[]}`), "m"); err == nil {
		t.Fatal("缺 model 字段应报错")
	}
	if _, err := RewriteModelInPlace([]byte(`[]`), "m"); err == nil {
		t.Fatal("顶层非对象应报错")
	}
}
