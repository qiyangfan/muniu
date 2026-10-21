// conformance 回放测试：遍历 conformance/<provider>/<scenario>.json，
// 非流式断言 NormalizeUsage，流式逐事件断言 meta 与 passthrough 字节等价。
// fixture 是独立资产，改动时必须同步 quirk。
package adapter_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/solvela/muniu/internal/adapter"
	"github.com/solvela/muniu/internal/adapter/bridge"
	"github.com/solvela/muniu/internal/protocol"
)

// blank import 接线 anthropic 桥接工厂，anthropic fixture 需要
var _ = bridge.NewAnthropic

// conformanceFixture 是 fixture 文件形态，与 conformance/schema.json 对齐。
type conformanceFixture struct {
	Provider    string          `json:"provider"`
	Scenario    string          `json:"scenario"`
	SourceURL   string          `json:"source_url"`
	Inbound     json.RawMessage `json:"inbound"`
	UpstreamRaw struct {
		Stream []string        `json:"stream"`
		Status int             `json:"status"`
		Body   json.RawMessage `json:"body"`
	} `json:"upstream_raw"`
	Expect struct {
		Usage               *protocol.Usage     `json:"usage"`
		Events              []adapter.EventMeta `json:"events"`
		PassthroughLossless *bool               `json:"passthrough_lossless"`
	} `json:"expect"`
}

func TestConformance(t *testing.T) {
	root := filepath.Join("..", "..", "conformance")
	paths := globFixtures(t, root)
	if len(paths) < 6 {
		t.Fatalf("首批 fixture 应 ≥6 个,实际 %d", len(paths))
	}
	for _, p := range paths {
		fix := loadFixture(t, root, p)
		t.Run(fix.Provider+"/"+fix.Scenario, func(t *testing.T) {
			replay(t, fix)
		})
	}
}

// globFixtures 收集 conformance/*/*.json，排除 README 与 schema。
func globFixtures(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("读取 conformance 目录: %v", err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		files, err := filepath.Glob(filepath.Join(root, e.Name(), "*.json"))
		if err != nil {
			t.Fatal(err)
		}
		sort.Strings(files)
		out = append(out, files...)
	}
	return out
}

// loadFixture 读取并做结构性校验，含必填字段与文件名一致性；等价 schema 的最小手写检查，
// 不引第三方 jsonschema 依赖。
func loadFixture(t *testing.T, root, path string) *conformanceFixture {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 fixture: %v", err)
	}
	var f conformanceFixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("fixture 非法 JSON: %v", err)
	}
	hasStream := len(f.UpstreamRaw.Stream) > 0
	if f.Provider == "" || f.Scenario == "" || f.SourceURL == "" ||
		len(f.Inbound) == 0 || (!hasStream && len(f.UpstreamRaw.Body) == 0) ||
		(!hasStream && f.Expect.Usage == nil) {
		t.Fatalf("fixture 必填字段缺失(provider/scenario/source_url/inbound/upstream_raw/expect): %s", path)
	}
	if !strings.HasPrefix(f.SourceURL, "https://") {
		t.Fatalf("source_url 必须是官方 https 文档链接: %s", f.SourceURL)
	}
	rel, _ := filepath.Rel(root, path)
	want := f.Provider + string(filepath.Separator) + f.Scenario + ".json"
	if rel != want {
		t.Fatalf("文件路径 %s 与 provider/scenario 不符,期望 %s", rel, want)
	}
	return &f
}

func replay(t *testing.T, f *conformanceFixture) {
	t.Helper()
	ad, err := adapter.For(f.Provider, "https://upstream.invalid/v1", "conformance-key")
	if err != nil {
		t.Fatalf("构造适配器: %v", err)
	}

	switch {
	case len(f.UpstreamRaw.Stream) > 0:
		replayStream(t, ad, f)
	case len(f.UpstreamRaw.Body) > 0:
		replayNonStream(t, ad, f)
	default:
		t.Fatal("fixture 须提供 stream 或 body 之一")
	}
}

// replayStream 流式回放：可见事件 meta 对齐，passthrough 逐字节比对且可关闭，usage 帧归一。
// 可见事件指非零翻译，桥接的 start、stop、ping 等零输出事件不进入断言序列。
func replayStream(t *testing.T, ad adapter.Adapter, f *conformanceFixture) {
	t.Helper()
	lossless := f.Expect.PassthroughLossless == nil || *f.Expect.PassthroughLossless

	var usageFrames []json.RawMessage
	var visible []adapter.EventMeta
	for i, payload := range f.UpstreamRaw.Stream {
		passthrough, meta, err := ad.StreamEvent([]byte(payload))
		if err != nil {
			t.Fatalf("事件 %d 解析失败: %v", i, err)
		}
		if lossless && string(passthrough) != payload {
			t.Fatalf("事件 %d 透传被改写:\n got=%q\nwant=%q", i, passthrough, payload)
		}
		invisible := len(passthrough) == 0 && payload != "[DONE]"
		if !invisible {
			visible = append(visible, meta)
		}
		if meta.IsUsage {
			// usage 帧抽内层 usage 对象供归一；桥接事件抽翻译后的帧，与 proxy 同一取法
			src := payload
			if len(passthrough) > 0 && string(passthrough) != payload {
				src = string(passthrough)
			}
			var frame struct {
				Usage json.RawMessage `json:"usage"`
			}
			if err := json.Unmarshal([]byte(src), &frame); err == nil && len(frame.Usage) > 0 && string(frame.Usage) != "null" {
				usageFrames = append(usageFrames, frame.Usage)
			}
		}
	}
	if len(visible) != len(f.Expect.Events) {
		t.Fatalf("可见事件数不符: got=%d want=%d", len(visible), len(f.Expect.Events))
	}
	for i, want := range f.Expect.Events {
		if visible[i] != want {
			t.Fatalf("可见事件 %d meta 不符:\n got=%+v\nwant=%+v", i, visible[i], want)
		}
	}

	// usage 断言：多帧字段合并后比对，桥接的 input/output 分帧到达
	assertUsage(t, ad, usageFrames, f.Expect.Usage, f.Provider, f.Scenario)
}

// replayNonStream 非流式回放：body.usage 或 usageMetadata 归一断言，分别为 OpenAI 与 Gemini 形态。
func replayNonStream(t *testing.T, ad adapter.Adapter, f *conformanceFixture) {
	t.Helper()
	var body struct {
		Usage         json.RawMessage `json:"usage"`
		UsageMetadata json.RawMessage `json:"usageMetadata"` // Gemini GenerateContentResponse 键名
	}
	if err := json.Unmarshal(f.UpstreamRaw.Body, &body); err != nil {
		t.Fatalf("body 非法: %v", err)
	}
	usage := body.Usage
	if len(usage) == 0 {
		usage = body.UsageMetadata
	}
	assertUsage(t, ad, []json.RawMessage{usage}, f.Expect.Usage, f.Provider, f.Scenario)
}

// assertUsage 将归一后的 usage 与期望逐字段比对；期望为 nil 时跳过，无帧而期望存在时报错。
func assertUsage(t *testing.T, ad adapter.Adapter, frames []json.RawMessage, want *protocol.Usage, provider, scenario string) {
	t.Helper()
	if want == nil {
		return
	}
	if len(frames) == 0 {
		t.Fatalf("%s/%s: 期望 usage 但上游未提供(检查 include_usage 或 quirk 归一;对照 %s)",
			provider, scenario, sourceURLOf(provider, scenario))
	}
	got := protocol.Usage{}
	have := false
	for _, raw := range frames {
		u, err := ad.NormalizeUsage(raw)
		if err != nil {
			t.Fatalf("NormalizeUsage 失败: %v", err)
		}
		if v := u.PromptTokens; v != 0 {
			got.PromptTokens = v
		}
		if v := u.CompletionTokens; v != 0 {
			got.CompletionTokens = v
		}
		if v := u.ReasoningTokens; v != 0 {
			got.ReasoningTokens = v
		}
		if v := u.CachedTokens; v != 0 {
			got.CachedTokens = v
		}
		have = true
	}
	if !have {
		t.Fatalf("%s/%s: 无有效 usage 帧", provider, scenario)
	}
	if got != *want {
		t.Fatalf("usage 归一不符:\n got=%+v\nwant=%+v\n(溯源 %s)", got, *want, sourceURLOf(provider, scenario))
	}
}

// sourceURLOf 重读 fixture 的 source_url 供失败信息溯源（测试辅助）。
func sourceURLOf(provider, scenario string) string {
	raw, err := os.ReadFile(filepath.Join("..", "..", "conformance", provider, scenario+".json"))
	if err != nil {
		return "(fixture 不可读)"
	}
	var f struct {
		SourceURL string `json:"source_url"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return "(fixture 非法)"
	}
	if f.SourceURL == "" {
		return "(fixture 缺 source_url)"
	}
	return f.SourceURL
}
