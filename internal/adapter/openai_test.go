// openai 基座适配器契约测试：Prepare 出站形态、Classify 粗分、usage 归一、
// StreamEvent 只读归一且 passthrough 为原字节。
package adapter

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/solvela/muniu/internal/failover"
	"github.com/solvela/muniu/internal/protocol"
)

func TestOpenAI_Prepare(t *testing.T) {
	reqBody := `{"model":"alias-a","messages":[{"role":"user","content":"hi"}],"vendor_ext":{"k":1}}`
	req, err := protocol.ParseChatRequest([]byte(reqBody))
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name    string
		baseURL string
		wantURL string
	}{
		{"基址带版本路径", "https://api.deepseek.com/v1", "https://api.deepseek.com/v1/chat/completions"},
		{"基址尾斜杠去重", "https://api.x.com/v1/", "https://api.x.com/v1/chat/completions"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := NewOpenAI(c.baseURL, "sk-test")
			out, err := a.Prepare(req, "upstream-a", "sk-test")
			if err != nil {
				t.Fatalf("Prepare 失败: %v", err)
			}
			if out.URL != c.wantURL {
				t.Fatalf("URL 拼接不符: %q", out.URL)
			}
			if got := out.Header.Get("Authorization"); got != "Bearer sk-test" {
				t.Fatalf("鉴权头不符: %q", got)
			}
			if got := out.Header.Get("Content-Type"); got != "application/json" {
				t.Fatalf("Content-Type 不符: %q", got)
			}
			// 出站 body 只换 model，未知字段零丢失
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(out.Body, &fields); err != nil {
				t.Fatalf("出站 body 非法: %v", err)
			}
			var m string
			_ = json.Unmarshal(fields["model"], &m)
			if m != "upstream-a" {
				t.Fatalf("model 未改写: %q", m)
			}
			if string(fields["vendor_ext"]) != `{"k":1}` {
				t.Fatalf("未知字段丢失: %s", fields["vendor_ext"])
			}
		})
	}
}

func TestOpenAI_Classify_状态码粗分(t *testing.T) {
	a := NewOpenAI("https://api.x.com/v1", "k")
	cases := []struct {
		status int
		want   failover.ErrorClass
	}{
		{400, failover.ClassBadRequest},
		{401, failover.ClassAuthFailed},
		{403, failover.ClassAuthFailed},
		{404, failover.ClassUnknown},
		{429, failover.ClassRateLimited},
		{500, failover.ClassUpstream5xx},
		{502, failover.ClassUpstream5xx},
		{503, failover.ClassUpstream5xx},
		{200, failover.ClassUnknown},
	}
	for _, c := range cases {
		if got := a.Classify(c.status, []byte(`{}`)); got != c.want {
			t.Fatalf("Classify(%d)=%s, 期望 %s", c.status, got, c.want)
		}
	}
}

func TestOpenAI_NormalizeUsage_归一表(t *testing.T) {
	a := NewOpenAI("https://api.x.com/v1", "k")
	cases := []struct {
		name string
		raw  string
		want protocol.Usage
	}{
		{
			name: "完整 details",
			raw:  `{"prompt_tokens":10,"completion_tokens":20,"prompt_tokens_details":{"cached_tokens":4},"completion_tokens_details":{"reasoning_tokens":6}}`,
			want: protocol.Usage{PromptTokens: 10, CompletionTokens: 20, ReasoningTokens: 6, CachedTokens: 4},
		},
		{
			name: "details 缺失按 0",
			raw:  `{"prompt_tokens":10,"completion_tokens":20}`,
			want: protocol.Usage{PromptTokens: 10, CompletionTokens: 20},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := a.NormalizeUsage(json.RawMessage(c.raw))
			if err != nil {
				t.Fatalf("归一失败: %v", err)
			}
			if got != c.want {
				t.Fatalf("归一结果不符: %+v, 期望 %+v", got, c.want)
			}
		})
	}

	if _, err := a.NormalizeUsage(json.RawMessage(`not-json`)); err == nil {
		t.Fatal("非法 usage 必须报错(估算路径兜底由上层处理)")
	}
}

func TestOpenAI_StreamEvent_passthrough原字节(t *testing.T) {
	a := NewOpenAI("https://api.x.com/v1", "k")
	cases := []struct {
		name       string
		data       string
		hasContent bool
		isUsage    bool
		isDone     bool
	}{
		{"role 帧不触发首token", `{"choices":[{"delta":{"role":"assistant"}}]}`, false, false, false},
		{"content 帧触发首token", `{"choices":[{"delta":{"content":"你"}}]}`, true, false, false},
		{"reasoning_content 帧触发首token", `{"choices":[{"delta":{"reasoning_content":"嗯"}}]}`, true, false, false},
		{"usage 终帧", `{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":2}}`, false, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			raw := []byte(c.data)
			passthrough, meta, err := a.StreamEvent(raw)
			if err != nil {
				t.Fatalf("StreamEvent 失败: %v", err)
			}
			// passthrough 为原字节零改写
			if string(passthrough) != c.data {
				t.Fatalf("passthrough 被改写: %q → %q", c.data, passthrough)
			}
			if meta.HasContent != c.hasContent || meta.IsUsage != c.isUsage || meta.IsDone != c.isDone {
				t.Fatalf("meta 不符: %+v", meta)
			}
		})
	}

	t.Run("DONE 帧", func(t *testing.T) {
		passthrough, meta, err := a.StreamEvent([]byte("[DONE]"))
		if err != nil || !meta.IsDone || string(passthrough) != "[DONE]" {
			t.Fatalf("DONE 判定不符: %q %+v %v", passthrough, meta, err)
		}
	})

	t.Run("非法 JSON 报错", func(t *testing.T) {
		if _, _, err := a.StreamEvent([]byte("{bad")); err == nil {
			t.Fatal("非法帧必须报错(供上层归 mid_stream_fail)")
		}
	})
}

func TestRegistry_注册与获取(t *testing.T) {
	r := NewRegistry()
	r.Register("deepseek", NewOpenAI("https://api.deepseek.com/v1", "k"))
	if _, ok := r.Get("deepseek"); !ok {
		t.Fatal("已注册 provider 必须可取到")
	}
	if _, ok := r.Get("nope"); ok {
		t.Fatal("未注册 provider 不得返回")
	}
	if !strings.Contains(r.Names()[0], "deepseek") {
		t.Fatalf("Names 列表不符: %v", r.Names())
	}
}

func TestOpenAI_PrepareEmbeddings(t *testing.T) {
	a := NewOpenAI("https://api.x.com/v1/", "sk-emb")
	req, err := protocol.ParseEmbeddingRequest([]byte(`{"model":"alias","input":"你好","vendor_ext":1}`))
	if err != nil {
		t.Fatal(err)
	}
	out, err := a.PrepareEmbeddings(req, "text-embedding-3-small", "sk-emb")
	if err != nil {
		t.Fatal(err)
	}
	if out.URL != "https://api.x.com/v1/embeddings" {
		t.Fatalf("embeddings URL 不符: %s", out.URL)
	}
	if out.Header.Get("Authorization") != "Bearer sk-emb" {
		t.Fatalf("鉴权头不符: %v", out.Header)
	}
	// 只换 model，input 与未知字段零丢失
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(out.Body, &fields)
	var m, inp string
	_ = json.Unmarshal(fields["model"], &m)
	_ = json.Unmarshal(fields["input"], &inp)
	if m != "text-embedding-3-small" || inp != "你好" {
		t.Fatalf("改写不符: model=%q input=%s", m, inp)
	}
}

// 空 data 或纯注释事件是 SSE 心跳，不得报错；按解析失败处理会把合法心跳误判为上游断流。
func TestOpenAI_StreamEvent_空载荷心跳(t *testing.T) {
	o := &OpenAI{}
	for _, in := range [][]byte{nil, []byte(""), []byte(": keep-alive")} {
		pt, meta, err := o.StreamEvent(in)
		if err != nil || meta.HasContent || meta.IsUsage || meta.IsDone {
			t.Fatalf("空载荷应不可见且不报错: in=%q pt=%q meta=%+v err=%v", in, pt, meta, err)
		}
	}
}

// DeepSeek 402 余额不足必须归限流而非未知：欠费 key 走冷却自动重探，充值即恢复；
// 归 ClassUnknown 会让欠费 key 被持续选中重试且不触发任何冷却。
func TestQuirk_DeepSeek402归类限流(t *testing.T) {
	ad, err := For("deepseek", "https://api.deepseek.com/v1", "k")
	if err != nil {
		t.Fatal(err)
	}
	if got := ad.Classify(402, nil); got != failover.ClassRateLimited {
		t.Fatalf("402 应归 rate_limited: %v", got)
	}
	// 其余状态码仍走基座粗分表，quirk 只声明差异
	if got := ad.Classify(401, nil); got != failover.ClassAuthFailed {
		t.Fatalf("401 应仍归 auth_failed: %v", got)
	}
	if got := ad.Classify(500, nil); got != failover.ClassUpstream5xx {
		t.Fatalf("5xx 应仍归 upstream_5xx: %v", got)
	}
	if got := ad.Classify(429, nil); got != failover.ClassRateLimited {
		t.Fatalf("429 应仍归 rate_limited: %v", got)
	}
	// OpenAI 基座将 402 归为 unknown；DeepSeek 的覆盖行为由适配器测试验证。
	base := &OpenAI{}
	if got := base.Classify(402, nil); got != failover.ClassUnknown {
		t.Fatalf("基座 402 应维持 unknown: %v", got)
	}
}
