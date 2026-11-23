// Gemini 同构直通 e2e：非流式/流式原始体与响应保真、失格部件原生服务、
// failover 换道回落翻译与失格升格。Gemini 入站 × gemini 候选走直通，跨协议仍单跳翻译。
package proxy

import (
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/solvela/muniu/internal/adapter"
	"github.com/solvela/muniu/internal/config"
	"github.com/solvela/muniu/internal/keypool"
	"github.com/solvela/muniu/internal/testkit"
)

// newGeminiFailoverProxy gemini 主候选 + openai 备候选：验证换道后重新判定路径。
func newGeminiFailoverProxy(t *testing.T, gemUp, oaiUp *testkit.Upstream) *Proxy {
	t.Helper()
	reg := adapter.NewRegistry()
	reg.Register("gem", adapter.NewGemini(gemUp.URL()))
	reg.Register("oai", adapter.NewOpenAI(oaiUp.URL(), ""))
	return New(Config{
		Registry: reg,
		Providers: []config.Provider{
			{Name: "gem", Adapter: "gemini"},
			{Name: "oai", Adapter: "openai"},
		},
		Routes: map[string][]config.Candidate{
			"gemini-x": {
				{Provider: "gem", Model: "up-g", Priority: 0},
				{Provider: "oai", Model: "up-o", Priority: 1},
			},
		},
		Timeouts: Timeouts{Dial: time.Second, FirstToken: 10 * time.Second, ChunkIdle: 10 * time.Second},
		Pools: map[string]*keypool.Pool{
			"gem": keypool.NewPool([]keypool.KeyInput{{ID: "KG", Value: "gk", Weight: 1}}),
			"oai": keypool.NewPool([]keypool.KeyInput{{ID: "KO", Value: "ok", Weight: 1}}),
		},
		RandIntn: func(n int) int { return 0 },
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
}

// TestGeminiE2E_直通非流式 fileData 等失格部件经 gemini 候选原生服务；
// 出站原始体与响应逐字节保真，不触发 content_part_unsupported。
func TestGeminiE2E_直通非流式(t *testing.T) {
	const upstreamBody = `{"candidates":[{"content":{"parts":[{"text":"看到了"}],"role":"model"},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":8},"modelVersion":"up-g","safetyRatings":[{"category":"HARM_CATEGORY_HARASSMENT","probability":"NEGLIGIBLE"}]}`
	up := testkit.StartJSONUpstream(t, 200, upstreamBody)
	p := newGeminiProxy(t, up)
	ts := mountGemini(t, p)

	raw := `{"contents":[{"role":"user","parts":[{"fileData":{"fileUri":"gs://bucket/doc.pdf"}},{"text":"看图"}]}],"safetySettings":[{"category":"HARM_CATEGORY_HARASSMENT","threshold":"BLOCK_NONE"}]}`
	status, body := postGemini(t, ts.URL, "gemini-x:generateContent", raw, http.Header{}, "")
	if status != http.StatusOK {
		t.Fatalf("直通应 200: %d %s", status, body)
	}
	if body != upstreamBody {
		t.Fatalf("非流式响应应逐字节回写:\n got=%s\nwant=%s", body, upstreamBody)
	}
	if got := string(up.LastBody()); got != raw {
		t.Fatalf("出站原始体应逐字节保留:\n got=%s\nwant=%s", got, raw)
	}
	if got, want := up.LastURL(), "/models/up-g:generateContent"; got != want {
		t.Fatalf("出站 URL 不符: got=%s want=%s", got, want)
	}
	if got := up.LastHeader("x-goog-api-key"); got != "gk" {
		t.Fatalf("鉴权头不符: %q", got)
	}
}

// TestGeminiE2E_直通流式 上游 Gemini SSE 帧逐字节透传，不合成终帧、不泄漏 [DONE]。
func TestGeminiE2E_直通流式(t *testing.T) {
	events := []testkit.Event{
		{Data: "data: " + `{"candidates":[{"content":{"parts":[{"text":"今天"}],"role":"model"},"index":0}]}` + "\n\n"},
		{Data: "data: " + `{"candidates":[{"content":{"parts":[{"text":"晴天"}],"role":"model"},"index":0}]}` + "\n\n"},
		{Data: "data: " + `{"candidates":[{"content":{"parts":[]},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":8}}` + "\n\n"},
	}
	up := testkit.StartStreamUpstream(t, events)
	p := newGeminiProxy(t, up)
	ts := mountGemini(t, p)

	raw := `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"tools":[{"functionDeclarations":[{"name":"w","parameters":{"type":"object"}}]}]}`
	status, body := postGemini(t, ts.URL, "gemini-x:streamGenerateContent", raw, http.Header{}, "?alt=sse")
	if status != http.StatusOK {
		t.Fatalf("直通流应 200: %d %s", status, body)
	}
	want := events[0].Data + events[1].Data + events[2].Data
	if body != want {
		t.Fatalf("流式帧应逐字节透传:\n got=%q\nwant=%q", body, want)
	}
	if strings.Contains(body, "[DONE]") {
		t.Fatalf("Gemini 直通流不得合成 [DONE]: %s", body)
	}
	if got, wantURL := up.LastURL(), "/models/up-g:streamGenerateContent?alt=sse"; got != wantURL {
		t.Fatalf("流式出站 URL 不符: got=%s want=%s", got, wantURL)
	}
	if got := string(up.LastBody()); got != raw {
		t.Fatalf("流式出站原始体应逐字节保留: %s", got)
	}
}

// TestGeminiE2E_换道回落翻译 文本请求主候选 5xx 后换道 openai：第二候选走翻译路径，
// 客户端获得合法 Gemini 形态响应；Gemini 原始体只发往 gemini 候选。
func TestGeminiE2E_换道回落翻译(t *testing.T) {
	gemUp := testkit.StartJSONUpstream(t, 500, `{"error":{"code":500,"status":"UNAVAILABLE","message":"boom"}}`)
	oaiUp := testkit.StartJSONUpstream(t, 200, usageJSONBody)
	p := newGeminiFailoverProxy(t, gemUp, oaiUp)
	ts := mountGemini(t, p)

	raw := `{"contents":[{"role":"user","parts":[{"text":"你好"}]}]}`
	status, body := postGemini(t, ts.URL, "gemini-x:generateContent", raw, http.Header{}, "")
	if status != http.StatusOK {
		t.Fatalf("换道翻译应成功: %d %s", status, body)
	}
	if !strings.Contains(body, `"candidates"`) || !strings.Contains(body, `"text":"你好"`) {
		t.Fatalf("应反向映射为 Gemini 形态: %s", body)
	}
	if got := string(gemUp.LastBody()); got != raw {
		t.Fatalf("gemini 候选应收到原始体: %s", got)
	}
	if got := string(oaiUp.LastBody()); !strings.Contains(got, `"messages"`) {
		t.Fatalf("openai 候选应收到 canonical chat 体: %s", got)
	}
}

// TestGeminiE2E_直通治理回落 能力目录对 max_output_tokens 声明上限：gemini 原生请求按 canonical
// 评估命中治理干预，回落翻译路径并按约束改写，控制面语义与翻译路径一致。
func TestGeminiE2E_直通治理回落(t *testing.T) {
	up := testkit.StartJSONUpstream(t, 200, geminiJSONBody)
	reg := adapter.NewRegistry()
	reg.Register("gem", adapter.NewGemini(up.URL()))
	p := New(Config{
		Registry:  reg,
		Providers: []config.Provider{{Name: "gem", Adapter: "gemini"}},
		Routes: map[string][]config.Candidate{
			"gemini-x": {{Provider: "gem", Model: "up-g", Priority: 0}},
		},
		Capabilities: []config.ModelCapability{
			{Provider: "gem", Model: "up-g", MaxOutputTokens: 1024},
		},
		Timeouts: Timeouts{Dial: time.Second, FirstToken: 10 * time.Second, ChunkIdle: 10 * time.Second},
		Pools:    map[string]*keypool.Pool{"gem": keypool.NewPool([]keypool.KeyInput{{ID: "KG", Value: "gk", Weight: 1}})},
		RandIntn: func(n int) int { return 0 },
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ts := mountGemini(t, p)

	raw := `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"maxOutputTokens":4096}}`
	status, body := postGemini(t, ts.URL, "gemini-x:generateContent", raw, http.Header{}, "")
	if status != http.StatusOK {
		t.Fatalf("治理回落翻译应成功: %d %s", status, body)
	}
	upBody := string(up.LastBody())
	if strings.Contains(upBody, "4096") || !strings.Contains(upBody, `"maxOutputTokens":1024`) {
		t.Fatalf("治理应按 canonical 判据 clamp 后翻译出站: %s", upBody)
	}
	if up.RequestCount() != 1 {
		t.Fatalf("治理回落不得重试原生路径: count=%d", up.RequestCount())
	}
}

// TestGeminiE2E_直通空内容流 上游合法空内容流（finish/usage 帧、无内容帧）必须原样透传收尾，
// 不得判 mid_stream_fail。真实 Gemini maxOutputTokens 被 thinking 吃光即此形态。
func TestGeminiE2E_直通空内容流(t *testing.T) {
	finish := "data: " + `{"candidates":[{"content":{"parts":[{"text":""}],"role":"model"},"finishReason":"MAX_TOKENS","index":0}],"usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":0}}` + "\n\n"
	up := testkit.StartStreamUpstream(t, []testkit.Event{{Data: finish}})
	p := newGeminiProxy(t, up)
	ts := mountGemini(t, p)

	status, body := postGemini(t, ts.URL, "gemini-x:streamGenerateContent",
		`{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"maxOutputTokens":1}}`,
		http.Header{}, "?alt=sse")
	if status != http.StatusOK {
		t.Fatalf("空内容流应原样收尾: %d %s", status, body)
	}
	if body != finish {
		t.Fatalf("空内容流应逐字节透传:\n got=%q\nwant=%q", body, finish)
	}
}

// TestGeminiE2E_失格换道升格 fileData 请求主候选 5xx 后换道翻译候选：
// 出站前升格既有 400 content_part_unsupported，翻译候选上游零调用。
func TestGeminiE2E_失格换道升格(t *testing.T) {
	gemUp := testkit.StartJSONUpstream(t, 500, `{"error":{"code":500,"status":"UNAVAILABLE","message":"boom"}}`)
	oaiUp := testkit.StartJSONUpstream(t, 200, usageJSONBody)
	p := newGeminiFailoverProxy(t, gemUp, oaiUp)
	ts := mountGemini(t, p)

	raw := `{"contents":[{"role":"user","parts":[{"fileData":{"fileUri":"gs://bucket/doc.pdf"}}]}]}`
	status, body := postGemini(t, ts.URL, "gemini-x:generateContent", raw, http.Header{}, "")
	if status != http.StatusBadRequest {
		t.Fatalf("失格换道翻译候选应升格 400: %d %s", status, body)
	}
	if !strings.Contains(body, "content_part_unsupported") || !strings.Contains(body, `"status":"INVALID_ARGUMENT"`) {
		t.Fatalf("应返回 Gemini 失格信封: %s", body)
	}
	if oaiUp.RequestCount() != 0 {
		t.Fatalf("翻译候选上游必须零调用: %d", oaiUp.RequestCount())
	}
	if gemUp.RequestCount() != 1 || string(gemUp.LastBody()) != raw {
		t.Fatalf("gemini 候选应先收到原始体: count=%d body=%s", gemUp.RequestCount(), gemUp.LastBody())
	}
}
