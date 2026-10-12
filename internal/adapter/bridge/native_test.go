// 同构直通出站契约测试(protocol-native-passthrough;测试先行域):
// PrepareNative wire 保真(仅 model 原位手术,其余逐字节)、NativeStreamEvent meta-only
// 读侧(meta 与翻译字节解耦,客户端恒收原字节)、NormalizeNativeUsage 计量同口径。
package bridge_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/solvela/muniu/internal/adapter"
	"github.com/solvela/muniu/internal/adapter/bridge"
	"github.com/solvela/muniu/internal/protocol"
)

// TestAnthropic_PrepareNative_wire保真 原始体含多模态/扩展字段:出站除 model 值外逐字节相等,
// 鉴权头按声明形态(x-api-key 缺省 / bearer 分支),URL 拼 /v1/messages。
func TestAnthropic_PrepareNative_wire保真(t *testing.T) {
	raw := []byte(`{"model":"alias-a","max_tokens":64,"top_k":3,"metadata":{"user_id":"u"},"stop_sequences":["END"],` +
		`"system":[{"type":"text","text":"be nice","cache_control":{"type":"ephemeral"}}],` +
		`"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGk="}},` +
		`{"type":"text","text":"看图","cache_control":{"type":"ephemeral"}}]}]}`)

	t.Run("缺省x-api-key", func(t *testing.T) {
		out, err := bridge.NewAnthropic("https://up.invalid", adapter.Options{}).
			PrepareNative(&protocol.NativeRequest{Proto: protocol.InboundMessages, Raw: raw}, "claude-up", "sk-1")
		if err != nil {
			t.Fatal(err)
		}
		if out.URL != "https://up.invalid/v1/messages" {
			t.Fatalf("URL 不符: %s", out.URL)
		}
		if got := out.Header.Get("x-api-key"); got != "sk-1" {
			t.Fatalf("x-api-key 不符: %q", got)
		}
		if got := out.Header.Get("anthropic-version"); got == "" {
			t.Fatal("anthropic-version 头缺失")
		}
		want := strings.Replace(string(raw), `"model":"alias-a"`, `"model":"claude-up"`, 1)
		if string(out.Body) != want {
			t.Fatalf("wire 保真破坏:\n got=%s\nwant=%s", out.Body, want)
		}
	})

	t.Run("声明bearer", func(t *testing.T) {
		out, err := bridge.NewAnthropic("https://up.invalid", adapter.Options{Auth: adapter.AuthBearer}).
			PrepareNative(&protocol.NativeRequest{Proto: protocol.InboundMessages, Raw: raw}, "claude-up", "sk-2")
		if err != nil {
			t.Fatal(err)
		}
		if got := out.Header.Get("Authorization"); got != "Bearer sk-2" {
			t.Fatalf("Bearer 声明不符: %q", got)
		}
		if out.Header.Get("x-api-key") != "" {
			t.Fatal("bearer 声明下不得再发 x-api-key")
		}
	})

	t.Run("协议标签不符拒做", func(t *testing.T) {
		_, err := bridge.NewAnthropic("https://up.invalid", adapter.Options{}).
			PrepareNative(&protocol.NativeRequest{Proto: protocol.InboundResponses, Raw: raw}, "m", "k")
		if err == nil {
			t.Fatal("responses 入站对 anthropic 直通应拒绝")
		}
	})
}

// TestAnthropic_NativeStreamEvent_meta读侧 事件序列 meta 对照 + 内部 chunk 与 wire 解耦
// (翻译字节仅供计量/拼包,客户端恒收上游原字节——passthrough 语义由 proxy 保证)。
func TestAnthropic_NativeStreamEvent_meta读侧(t *testing.T) {
	ad := bridge.NewAnthropic("https://up.invalid", adapter.Options{})
	events := []struct {
		payload string
		want    adapter.EventMeta
	}{
		{`{"type":"message_start","message":{"usage":{"input_tokens":21,"cache_read_input_tokens":3}}}`, adapter.EventMeta{IsUsage: true}},
		{`{"type":"ping"}`, adapter.EventMeta{}},
		{`{"type":"content_block_start","index":0,"content_block":{"type":"text"}}`, adapter.EventMeta{}},
		{`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"你好"}}`, adapter.EventMeta{HasContent: true}},
		{`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"嗯"}}`, adapter.EventMeta{HasContent: true}},
		{`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`, adapter.EventMeta{IsUsage: true}},
		{`{"type":"message_stop"}`, adapter.EventMeta{IsDone: true}},
	}
	for i, e := range events {
		internal, meta, err := ad.NativeStreamEvent([]byte(e.payload))
		if err != nil {
			t.Fatalf("事件 %d: %v", i, err)
		}
		if meta != e.want {
			t.Fatalf("事件 %d meta 不符: got=%+v want=%+v", i, meta, e.want)
		}
		// 读侧契约:meta 与翻译字节解耦——内部 chunk 不得被误当作 wire(此处仅断言形状可解析)
		if meta.HasContent && len(internal) == 0 {
			t.Fatalf("事件 %d 有内容但内部 chunk 为空(计量拼包将静默归零)", i)
		}
	}
	// 流内 error 事件:诚实报错(与翻译路径同判定)
	if _, _, err := ad.NativeStreamEvent([]byte(`{"type":"error","error":{"message":"boom"}}`)); err == nil {
		t.Fatal("error 事件应报错")
	}
	// usage 归一同口径(anthropic 形态)
	u, err := ad.NormalizeNativeUsage(json.RawMessage(`{"input_tokens":21,"output_tokens":5,"cache_read_input_tokens":3}`))
	if err != nil || u.PromptTokens != 21 || u.CompletionTokens != 5 || u.CachedTokens != 3 {
		t.Fatalf("NormalizeNativeUsage 不符: %+v %v", u, err)
	}
}

// TestAnthropic_NativeFixture_conformance 语料冻结(conformance/anthropic/native/):
// 原始体含多模态/扩展字段的出站 wire 逐字节断言 + 事件序列 meta 对照,单源消费。
func TestAnthropic_NativeFixture_conformance(t *testing.T) {
	raw, err := os.ReadFile("../../../conformance/anthropic/native/messages-passthrough.json")
	if err != nil {
		t.Fatalf("读直通语料: %v", err)
	}
	var f struct {
		SourceURL string `json:"source_url"`
		NativeIn  struct {
			Raw string `json:"raw"` // 紧凑 JSON 字符串:字节级断言要求(嵌套对象经格式化会破坏逐字节比对)
		} `json:"native_in"`
		UpstreamModel string `json:"upstream_model"`
		Stream        []struct {
			Payload string            `json:"payload"`
			Meta    adapter.EventMeta `json:"meta"`
		} `json:"stream"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(f.SourceURL, "https://") {
		t.Fatalf("source_url 必须官方 https 锚点: %s", f.SourceURL)
	}
	out, err := bridge.NewAnthropic("https://up.invalid", adapter.Options{}).
		PrepareNative(&protocol.NativeRequest{Proto: protocol.InboundMessages, Raw: []byte(f.NativeIn.Raw)}, f.UpstreamModel, "k")
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(f.NativeIn.Raw, `"model":"alias-a"`, `"model":"`+f.UpstreamModel+`"`, 1)
	if string(out.Body) != want {
		t.Fatalf("语料 wire 保真破坏:\n got=%s\nwant=%s", out.Body, want)
	}
	ad := bridge.NewAnthropic("https://up.invalid", adapter.Options{})
	for i, e := range f.Stream {
		_, meta, err := ad.NativeStreamEvent([]byte(e.Payload))
		if err != nil {
			t.Fatalf("事件 %d: %v", i, err)
		}
		if meta != e.Meta {
			t.Fatalf("语料事件 %d meta 不符: got=%+v want=%+v", i, meta, e.Meta)
		}
	}
}

// 编译期契约断言:桥接实现直通能力接口。
var _ adapter.NativePreparer = (*bridge.Anthropic)(nil)
