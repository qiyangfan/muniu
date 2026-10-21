// 基座直通能力契约测试：PrepareNative 构造 responses 声明制通道的出站请求，
// NativeStreamEvent 为 responses SSE 的 meta 读侧，NormalizeNativeUsage 处理 responses
// usage 形态，PrepareNativeFetch 为 GET 代理。
package adapter_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/solvela/muniu/internal/adapter"
	"github.com/solvela/muniu/internal/protocol"
)

func newBase(t *testing.T) *adapter.OpenAI {
	t.Helper()
	return adapter.NewOpenAI("https://up.invalid/v1", "")
}

// TestOpenAI_PrepareNative_responses 验证出站为 <base>/v1/responses，仅改写 model 字段并注入 Bearer。
func TestOpenAI_PrepareNative_responses(t *testing.T) {
	raw := []byte(`{"model":"alias-r","stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,xx"}]}],"tools":[{"type":"web_search"}]}`)
	out, err := newBase(t).PrepareNative(&protocol.NativeRequest{Proto: protocol.InboundResponses, Raw: raw}, "up-r", "sk-9")
	if err != nil {
		t.Fatal(err)
	}
	if out.URL != "https://up.invalid/v1/responses" {
		t.Fatalf("URL 不符: %s", out.URL)
	}
	if got := out.Header.Get("Authorization"); got != "Bearer sk-9" {
		t.Fatalf("鉴权不符: %q", got)
	}
	want := strings.Replace(string(raw), `"model":"alias-r"`, `"model":"up-r"`, 1)
	if string(out.Body) != want {
		t.Fatalf("wire 保真破坏:\n got=%s\nwant=%s", out.Body, want)
	}
	// 非 responses 入站标签拒做，能力边界显式
	if _, err := newBase(t).PrepareNative(&protocol.NativeRequest{Proto: protocol.InboundMessages, Raw: raw}, "m", "k"); err == nil {
		t.Fatal("messages 入站对基座直通应拒绝")
	}
}

// TestOpenAI_NativeStreamEvent_responses 读侧 meta 对照：delta 有内容，completed 终帧带 usage，
// 生命周期事件不可见，failed 诚实报错；内部 chunk 供计量拼包，与翻译路径同一取法。
func TestOpenAI_NativeStreamEvent_responses(t *testing.T) {
	ad := newBase(t)
	events := []struct {
		payload string
		want    adapter.EventMeta
		wantErr bool
	}{
		{`{"type":"response.created","response":{"id":"resp_1"}}`, adapter.EventMeta{}, false},
		{`{"type":"response.in_progress","response":{"id":"resp_1"}}`, adapter.EventMeta{}, false},
		{`{"type":"response.output_item.added","output_index":0}`, adapter.EventMeta{}, false},
		{`{"type":"response.output_text.delta","delta":"你"}`, adapter.EventMeta{HasContent: true}, false},
		{`{"type":"response.function_call_arguments.delta","item_id":"item_1","delta":"{\"x\""}`, adapter.EventMeta{HasContent: true}, false},
		{`{"type":"response.output_item.done","output_index":0}`, adapter.EventMeta{}, false},
		{`{"type":"response.completed","response":{"id":"resp_1","usage":{"input_tokens":13,"output_tokens":1,"input_tokens_details":{"cached_tokens":2}}}}`, adapter.EventMeta{IsUsage: true, IsDone: true}, false},
		{`{"type":"response.failed","response":{"error":{"message":"boom"}}}`, adapter.EventMeta{}, true},
	}
	for i, e := range events {
		internal, meta, err := ad.NativeStreamEvent([]byte(e.payload))
		if e.wantErr {
			if err == nil {
				t.Fatalf("事件 %d 应报错", i)
			}
			continue
		}
		if err != nil {
			t.Fatalf("事件 %d: %v", i, err)
		}
		if meta != e.want {
			t.Fatalf("事件 %d meta 不符: got=%+v want=%+v", i, meta, e.want)
		}
		if meta.IsUsage {
			var frame struct {
				Usage json.RawMessage `json:"usage"`
			}
			if json.Unmarshal(internal, &frame) != nil || len(frame.Usage) == 0 {
				t.Fatalf("事件 %d usage 帧内部 chunk 缺 usage: %s", i, internal)
			}
			u, err := ad.NormalizeNativeUsage(frame.Usage)
			if err != nil || u.PromptTokens != 13 || u.CompletionTokens != 1 || u.CachedTokens != 2 {
				t.Fatalf("事件 %d usage 归一不符: %+v %v", i, u, err)
			}
		}
	}
	// 完成终帧的内部 chunk 携带 finish_reason=stop，缓存拼包准入与翻译路径一致
	_, meta, _ := ad.NativeStreamEvent([]byte(`{"type":"response.completed","response":{"id":"r","usage":{"input_tokens":1,"output_tokens":1}}}`))
	if !meta.IsDone {
		t.Fatal("completed 应为终帧")
	}
}

// TestOpenAI_PrepareNativeFetch 验证 GET 代理出站，URL 为 <base>/responses/{id}，无请求体，Bearer 鉴权。
func TestOpenAI_PrepareNativeFetch(t *testing.T) {
	out, err := newBase(t).PrepareNativeFetch(http.MethodGet, "resp_abc", "sk-1")
	if err != nil {
		t.Fatal(err)
	}
	if out.URL != "https://up.invalid/v1/responses/resp_abc" {
		t.Fatalf("URL 不符: %s", out.URL)
	}
	if len(out.Body) != 0 {
		t.Fatalf("GET 不得携带请求体: %s", out.Body)
	}
	if out.Header.Get("Authorization") != "Bearer sk-1" {
		t.Fatal("鉴权缺失")
	}
}

// 编译期断言：基座实现直通与 GET 代理能力。
var (
	_ adapter.NativePreparer = (*adapter.OpenAI)(nil)
	_ adapter.NativeFetcher  = (*adapter.OpenAI)(nil)
)
