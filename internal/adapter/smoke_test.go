//go:build smoke

// 真上游冒烟，手动档且 CI 禁跑，用法 make smoke PROVIDER=<名>
// 环境变量：
//
//	MUNIU_SMOKE_KEY      必填，真 key，ollama 等本地实现可用任意占位值
//	MUNIU_SMOKE_BASE_URL 必填，如 https://api.deepseek.com/v1
//	MUNIU_SMOKE_PROVIDER 适配器形态 openai/deepseek/moonshot/glm，缺省 openai
//	MUNIU_SMOKE_MODEL    上游模型名，缺省请求体自带；gemini 原生冒烟必填(如 gemini-3.5-flash)
package adapter_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/solvela/muniu/internal/adapter"
	"github.com/solvela/muniu/internal/adapter/bridge"
	"github.com/solvela/muniu/internal/protocol"
)

// blank import 接线 anthropic 桥接工厂
var _ = bridge.NewAnthropic

// smokeEnv 读取冒烟环境变量，缺配置则跳过，不 fail CI。
func smokeEnv(t *testing.T) (adapter.Adapter, string) {
	t.Helper()
	key := os.Getenv("MUNIU_SMOKE_KEY")
	base := os.Getenv("MUNIU_SMOKE_BASE_URL")
	if key == "" || base == "" {
		t.Skip("MUNIU_SMOKE_KEY / MUNIU_SMOKE_BASE_URL 未设置,跳过真上游冒烟")
	}
	provider := os.Getenv("MUNIU_SMOKE_PROVIDER")
	if provider == "" {
		provider = "openai"
	}
	// MUNIU_SMOKE_AUTH 鉴权形态声明，空为缺省，bearer 为国内 Anthropic 分支口径；透传 config.Provider.Auth
	ad, err := adapter.For(provider, base, key, adapter.Options{Auth: os.Getenv("MUNIU_SMOKE_AUTH")})
	if err != nil {
		t.Fatalf("构造适配器: %v", err)
	}
	return ad, os.Getenv("MUNIU_SMOKE_MODEL")
}

func TestSmoke_Env(t *testing.T) {
	ad, model := smokeEnv(t)
	smokeKey := os.Getenv("MUNIU_SMOKE_KEY")
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"只回复:pong"}]}`, model)
	req, err := protocol.ParseChatRequest([]byte(body))
	if err != nil {
		t.Fatal(err)
	}

	out, err := ad.Prepare(req, req.Model, os.Getenv("MUNIU_SMOKE_KEY"))
	if err != nil {
		t.Fatal(err)
	}

	// 非流式
	httpReq, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, out.URL, bytes.NewReader(out.Body))
	httpReq.Header = out.Header
	start := time.Now()
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		t.Fatalf("非流式请求失败: %v", err)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	t.Logf("非流式: status=%d 耗时=%s body=%s", resp.StatusCode, time.Since(start).Round(time.Millisecond), truncateSmk(b))
	if resp.StatusCode != 200 {
		t.Fatalf("非流式状态码 %d", resp.StatusCode)
	}
	var chatResp protocol.ChatResponse
	if err := json.Unmarshal(b, &chatResp); err != nil {
		t.Fatalf("非流式响应非 JSON: %v", err)
	}
	if usage, err := ad.NormalizeUsage(chatResp.Usage); err != nil {
		t.Logf("usage 归一失败(登记 quirk): %v", err)
	} else {
		t.Logf("非流式 usage: %+v", usage)
	}

	// 流式
	req.Stream = true
	out, err = ad.Prepare(req, req.Model, smokeKey)
	if err != nil {
		t.Fatal(err)
	}
	out.Body, _ = json.Marshal(func() any {
		var m map[string]json.RawMessage
		_ = json.Unmarshal(req.Raw, &m)
		m["stream"] = json.RawMessage("true")
		if model != "" {
			m["model"], _ = json.Marshal(model)
		}
		return m
	}())
	httpReq, _ = http.NewRequestWithContext(t.Context(), http.MethodPost, out.URL, bytes.NewReader(out.Body))
	httpReq.Header = out.Header
	resp, err = http.DefaultClient.Do(httpReq)
	if err != nil {
		t.Fatalf("流式请求失败: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Fatalf("流式状态码 %d", resp.StatusCode)
	}
	events, done := 0, false
	br := bufio.NewScanner(resp.Body)
	var lastPayload string
	for br.Scan() {
		line := strings.TrimSpace(br.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			done = true
			break
		}
		events++
		lastPayload = payload
	}
	t.Logf("流式: events=%d done=%v 末帧=%s", events, done, truncateSmk([]byte(lastPayload)))
	if events == 0 || !done {
		t.Fatalf("流式不完整: events=%d done=%v", events, done)
	}
}

func truncateSmk(b []byte) string {
	if len(b) > 300 {
		return string(b[:300]) + "…"
	}
	return string(b)
}

// 按供应商入口：make smoke PROVIDER=deepseek 跑 TestSmoke_deepseek，共用 TestSmoke_Env。
// PROVIDER 只选测试入口，适配器形态由 MUNIU_SMOKE_PROVIDER 决定，缺省 openai；
// 各家官方端点与模型示例见 docs/provider-presets.md。
func TestSmoke_deepseek(t *testing.T) { TestSmoke_Env(t) }
func TestSmoke_moonshot(t *testing.T) { TestSmoke_Env(t) }
func TestSmoke_glm(t *testing.T)      { TestSmoke_Env(t) }
func TestSmoke_ollama(t *testing.T)   { TestSmoke_Env(t) }
func TestSmoke_openai(t *testing.T)   { TestSmoke_Env(t) }

// 预设目录各家的冒烟入口，端点取自 docs/provider-presets.md 表 A/C。
func TestSmoke_qwen(t *testing.T)     { TestSmoke_Env(t) }
func TestSmoke_doubao(t *testing.T)   { TestSmoke_Env(t) }
func TestSmoke_byteplus(t *testing.T) { TestSmoke_Env(t) }
func TestSmoke_minimax(t *testing.T)  { TestSmoke_Env(t) }
func TestSmoke_stepfun(t *testing.T)  { TestSmoke_Env(t) }
func TestSmoke_qianfan(t *testing.T)  { TestSmoke_Env(t) }
func TestSmoke_hunyuan(t *testing.T)  { TestSmoke_Env(t) }
func TestSmoke_mimo(t *testing.T)     { TestSmoke_Env(t) }
func TestSmoke_longcat(t *testing.T)  { TestSmoke_Env(t) }
func TestSmoke_xai(t *testing.T)      { TestSmoke_Env(t) }

// TestSmoke_gemini 原生 Gemini 真上游冒烟。通用 TestSmoke_Env 按 OpenAI wire 解析响应并注入
// stream，对原生适配器不成立(ADR-0015)；本入口用 native wire 断言覆盖翻译出站与同构直通两径。
// 环境变量:MUNIU_SMOKE_KEY / MUNIU_SMOKE_BASE_URL / MUNIU_SMOKE_MODEL(必填)。
func TestSmoke_gemini(t *testing.T) {
	key := os.Getenv("MUNIU_SMOKE_KEY")
	base := os.Getenv("MUNIU_SMOKE_BASE_URL")
	model := os.Getenv("MUNIU_SMOKE_MODEL")
	if key == "" || base == "" {
		t.Skip("MUNIU_SMOKE_KEY / MUNIU_SMOKE_BASE_URL 未设置,跳过真上游冒烟")
	}
	if model == "" {
		t.Fatal("gemini 冒烟必须设置 MUNIU_SMOKE_MODEL(如 gemini-3.5-flash)")
	}
	ad, err := adapter.For("gemini", base, key)
	if err != nil {
		t.Fatal(err)
	}
	g, ok := ad.(*adapter.Gemini)
	if !ok {
		t.Fatalf("适配器形态不符: %T", ad)
	}

	// A. 翻译出站(非流式):canonical chat → Gemini wire → 原生响应反向映射
	req, err := protocol.ParseChatRequest([]byte(
		`{"model":"smoke","messages":[{"role":"user","content":"只回复:pong"}],"max_tokens":512,"temperature":0}`))
	if err != nil {
		t.Fatal(err)
	}
	out, err := ad.Prepare(req, model, key)
	if err != nil {
		t.Fatal(err)
	}
	status, body := smokeGeminiPost(t, out)
	chatBody, usageRaw, err := g.RewriteResponse(body)
	if err != nil {
		t.Fatalf("非流式反向映射失败: %v body=%s", err, truncateSmk(body))
	}
	var chat protocol.ChatResponse
	if err := json.Unmarshal(chatBody, &chat); err != nil {
		t.Fatalf("反向映射非 JSON: %v", err)
	}
	usage, err := g.NormalizeUsage(usageRaw)
	if err != nil {
		t.Fatalf("usage 归一失败: %v", err)
	}
	t.Logf("翻译非流式: status=%d choices=%d usage=%+v", status, len(chat.Choices), usage)
	if len(chat.Choices) == 0 || usage.PromptTokens == 0 {
		t.Fatalf("翻译非流式响应不完整: choices=%d usage=%+v", len(chat.Choices), usage)
	}

	// B. 翻译出站(流式):Gemini SSE → canonical 内部帧，usage 帧须可归一
	req.Stream = true
	out, err = ad.Prepare(req, model, key)
	if err != nil {
		t.Fatal(err)
	}
	status, body = smokeGeminiPost(t, out)
	seq := g.NewStreamSequencer()
	events, content, hasUsage := smokeGeminiSSE(t, body, func(p []byte) (adapter.EventMeta, error) {
		_, meta, err := seq(p)
		return meta, err
	})
	t.Logf("翻译流式: status=%d events=%d content=%v usage_frame=%v", status, events, content, hasUsage)
	if events == 0 || !content || !hasUsage {
		t.Fatalf("翻译流式不完整: events=%d content=%v usage=%v", events, content, hasUsage)
	}

	// C. 原生直通(非流式):原始体直出，模型在 URL，usageMetadata 归一
	raw := []byte(`{"contents":[{"role":"user","parts":[{"text":"只回复:pong"}]}],"generationConfig":{"maxOutputTokens":512,"temperature":0}}`)
	nout, err := g.PrepareNative(&protocol.NativeRequest{Proto: protocol.InboundGemini, Raw: raw}, model, key)
	if err != nil {
		t.Fatal(err)
	}
	status, nbody := smokeGeminiPost(t, nout)
	var nativeResp struct {
		Candidates    []json.RawMessage `json:"candidates"`
		UsageMetadata json.RawMessage   `json:"usageMetadata"`
		ModelVersion  string            `json:"modelVersion"`
	}
	if err := json.Unmarshal(nbody, &nativeResp); err != nil {
		t.Fatalf("原生非流式非 JSON: %v body=%s", err, truncateSmk(nbody))
	}
	nu, err := g.NormalizeNativeUsage(nativeResp.UsageMetadata)
	if err != nil {
		t.Fatalf("原生 usage 归一失败: %v", err)
	}
	t.Logf("原生直通非流式: status=%d candidates=%d model=%s usage=%+v",
		status, len(nativeResp.Candidates), nativeResp.ModelVersion, nu)
	if status != 200 || len(nativeResp.Candidates) == 0 || nu.PromptTokens == 0 {
		t.Fatalf("原生非流式响应不完整: status=%d candidates=%d usage=%+v",
			status, len(nativeResp.Candidates), nu)
	}

	// D. 原生直通(流式):原始体直出，SSE 帧原样，读侧 meta 须有内容与 usage
	nout, err = g.PrepareNative(&protocol.NativeRequest{Proto: protocol.InboundGemini, Raw: raw, Stream: true}, model, key)
	if err != nil {
		t.Fatal(err)
	}
	status, nbody = smokeGeminiPost(t, nout)
	events, content, hasUsage = smokeGeminiSSE(t, nbody, func(p []byte) (adapter.EventMeta, error) {
		_, meta, err := g.NativeStreamEvent(p)
		return meta, err
	})
	t.Logf("原生直通流式: status=%d events=%d content=%v usage_frame=%v", status, events, content, hasUsage)
	if status != 200 || events == 0 || !content || !hasUsage {
		t.Fatalf("原生流式不完整: status=%d events=%d content=%v usage=%v",
			status, events, content, hasUsage)
	}
}

// smokeGeminiPost 发送出站请求；429/503 视为高需求抖动，重试一次并留痕。
func smokeGeminiPost(t *testing.T, out *adapter.Outbound) (int, []byte) {
	t.Helper()
	for attempt := 0; ; attempt++ {
		httpReq, err := http.NewRequestWithContext(t.Context(), http.MethodPost, out.URL, bytes.NewReader(out.Body))
		if err != nil {
			t.Fatal(err)
		}
		httpReq.Header = out.Header
		resp, err := http.DefaultClient.Do(httpReq)
		if err != nil {
			t.Fatalf("出站请求失败: %v", err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if (resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable) && attempt == 0 {
			t.Logf("上游 %d(高需求抖动),重试一次: %s", resp.StatusCode, truncateSmk(body))
			time.Sleep(3 * time.Second)
			continue
		}
		t.Logf("出站 %s -> status=%d body=%s", out.URL, resp.StatusCode, truncateSmk(body))
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("出站状态码 %d", resp.StatusCode)
		}
		return resp.StatusCode, body
	}
}

// smokeGeminiSSE 逐帧交给协议读侧，统计事件、内容帧与 usage 帧。
func smokeGeminiSSE(t *testing.T, body []byte,
	handle func(payload []byte) (adapter.EventMeta, error)) (events int, content, usage bool) {
	t.Helper()
	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		meta, err := handle([]byte(payload))
		if err != nil {
			t.Fatalf("帧读侧失败: %v payload=%s", err, truncateSmk([]byte(payload)))
		}
		events++
		content = content || meta.HasContent
		usage = usage || meta.IsUsage
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("流读取失败: %v", err)
	}
	return events, content, usage
}
func TestSmoke_siliconflow(t *testing.T) { TestSmoke_Env(t) }
func TestSmoke_modelscope(t *testing.T)  { TestSmoke_Env(t) }
func TestSmoke_openrouter(t *testing.T)  { TestSmoke_Env(t) }
func TestSmoke_nvidia(t *testing.T)      { TestSmoke_Env(t) }
