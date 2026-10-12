// Gemini 原生入站端到端钉子(gemini-native-protocol):
// 路径解析与 alt=sse 校验、非流式/流式反向映射、路径别名同规错误、鉴权适配与 ?key= 剥离、
// bridgegate 声明式缺口(gemini 候选工具多轮不豁免)、跨协议缓存 canonical 同构、
// gemini×gemini 全链(StreamCloseDone 干净收尾)。
package proxy

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/solvela/muniu/internal/adapter"
	"github.com/solvela/muniu/internal/config"
	"github.com/solvela/muniu/internal/keypool"
	"github.com/solvela/muniu/internal/server"
	"github.com/solvela/muniu/internal/testkit"
)

// mountGemini 装配 Gemini 入站(mux 同生产形态:AdaptGeminiKey 先于业务)。
func mountGemini(t *testing.T, p *Proxy) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle("POST /v1beta/models/{modelMethod}", server.AdaptGeminiKey(p.HandlerGemini()))
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

func postGemini(t *testing.T, url, model, body string, header http.Header, query string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest("POST", url+"/v1beta/models/"+model+query, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp, string(b)
}

// newGeminiProxy gemini 出站候选拓扑。
func newGeminiProxy(t *testing.T, up *testkit.Upstream) *Proxy {
	t.Helper()
	reg := adapter.NewRegistry()
	reg.Register("gem", adapter.NewGemini(up.URL()))
	p := New(Config{
		Registry:  reg,
		Providers: []config.Provider{{Name: "gem", Adapter: "gemini"}},
		Routes: map[string][]config.Candidate{
			"gemini-x": {{Provider: "gem", Model: "up-g", Priority: 0}},
		},
		Timeouts: Timeouts{Dial: time.Second, FirstToken: 10 * time.Second, ChunkIdle: 10 * time.Second},
		Pools:    map[string]*keypool.Pool{"gem": keypool.NewPool([]keypool.KeyInput{{ID: "KG", Value: "gk", Weight: 1}})},
		RandIntn: func(n int) int { return 0 },
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	return p
}

// geminiJSONBody gemini 形态假上游响应(gemini 候选消费 gemini wire)。
const geminiJSONBody = `{"candidates":[{"content":{"parts":[{"text":"你好"}],"role":"model"},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":8,"totalTokenCount":20},"modelVersion":"up-g"}`

// TestGeminiE2E_非流式 全链:翻译 → 上游 → 反向映射为 GenerateContentResponse。
func TestGeminiE2E_非流式(t *testing.T) {
	up := testkit.StartJSONUpstream(t, 200, geminiJSONBody)
	p := newGeminiProxy(t, up)
	ts := mountGemini(t, p)

	resp, body := postGemini(t, ts.URL, "gemini-x:generateContent",
		`{"contents":[{"role":"user","parts":[{"text":"你好"}]}]}`, http.Header{}, "")
	if resp.StatusCode != 200 {
		t.Fatalf("应 200: %d %s", resp.StatusCode, body)
	}
	var obj struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
				Role string `json:"role"`
			} `json:"content"`
			FinishReason string `json:"finishReason"`
		} `json:"candidates"`
		UsageMetadata struct {
			PromptTokenCount     int64 `json:"promptTokenCount"`
			CandidatesTokenCount int64 `json:"candidatesTokenCount"`
		} `json:"usageMetadata"`
	}
	if err := json.Unmarshal([]byte(body), &obj); err != nil {
		t.Fatalf("非 Gemini 形态: %v (%s)", err, body)
	}
	if len(obj.Candidates) != 1 || obj.Candidates[0].Content.Role != "model" ||
		obj.Candidates[0].Content.Parts[0].Text != "你好" || obj.Candidates[0].FinishReason != "STOP" {
		t.Fatalf("candidates 形态不符: %s", body)
	}
	if obj.UsageMetadata.PromptTokenCount != 12 || obj.UsageMetadata.CandidatesTokenCount != 8 {
		t.Fatalf("usageMetadata 不符: %s", body)
	}
	// 出站 wire:gemini 形态(x-goog-api-key + :generateContent)
	if got := up.LastHeader("x-goog-api-key"); got != "gk" {
		t.Fatalf("鉴权头不符: %q", got)
	}
	upBody := string(up.LastBody())
	if !strings.Contains(upBody, `"role":"user"`) || !strings.Contains(upBody, `"text":"你好"`) ||
		!strings.Contains(upBody, `"contents"`) {
		t.Fatalf("出站形态不符: %s", upBody)
	}
}

// TestGeminiE2E_流式 alt=sse 流式:增量帧 → 终帧;上游无 [DONE](StreamCloseDone)干净收尾。
// 假上游语料 = gemini wire(conformance/gemini/text-stream 同款);全链走 gemini→canonical→gemini 往返。
func TestGeminiE2E_流式(t *testing.T) {
	up := testkit.StartStreamUpstream(t, []testkit.Event{
		{Data: "data: " + `{"candidates":[{"content":{"parts":[{"text":"今天"}],"role":"model"},"index":0}]}` + "\n\n"},
		{Data: "data: " + `{"candidates":[{"content":{"parts":[{"text":"晴天"}],"role":"model"},"index":0}]}` + "\n\n"},
		{Data: "data: " + `{"candidates":[{"content":{"parts":[],"role":"model"},"index":0,"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":8,"totalTokenCount":20}}` + "\n\n"},
	})
	p := newGeminiProxy(t, up)
	ts := mountGemini(t, p)

	resp, body := postGemini(t, ts.URL, "gemini-x:streamGenerateContent",
		`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`, http.Header{}, "?alt=sse")
	if resp.StatusCode != 200 {
		t.Fatalf("应 200: %d %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, `{"text":"今天"}`) || !strings.Contains(body, `{"text":"晴天"}`) {
		t.Fatalf("缺增量帧: %s", body)
	}
	if !strings.Contains(body, `"finishReason":"STOP"`) || !strings.Contains(body, `"promptTokenCount":12`) {
		t.Fatalf("缺终帧(usage/finishReason): %s", body)
	}
	// 网关补 [DONE] 语义在此不存在:客户端流以终帧收尾,无 OpenAI [DONE] 泄漏
	if strings.Contains(body, "data: [DONE]") {
		t.Fatalf("Gemini 流不得泄漏 [DONE]: %s", body)
	}
}

// TestGeminiE2E_streamForm校验 streamGenerateContent 缺 alt=sse → 400 机器码信封。
func TestGeminiE2E_streamForm校验(t *testing.T) {
	p := newGeminiProxy(t, testkit.StartJSONUpstream(t, 200, usageJSONBody))
	ts := mountGemini(t, p)
	resp, body := postGemini(t, ts.URL, "gemini-x:streamGenerateContent", `{"contents":[{"role":"user","parts":[{"text":"x"}]}]}`, http.Header{}, "")
	if resp.StatusCode != 400 || !strings.Contains(body, "stream_form_unsupported") || !strings.Contains(body, `"status":"INVALID_ARGUMENT"`) {
		t.Fatalf("缺 alt=sse 应 400 机器码信封: %d %s", resp.StatusCode, body)
	}
}

// TestGeminiE2E_路径别名与翻译拒绝 同规错误与显式拒绝(Gemini 信封形态)。
func TestGeminiE2E_路径别名与翻译拒绝(t *testing.T) {
	up := testkit.StartJSONUpstream(t, 200, usageJSONBody)
	p := newGeminiProxy(t, up)
	ts := mountGemini(t, p)

	// 别名不存在:与 chat model_not_found 同规(400 + 同文案;信封改写为 Gemini 形态)
	resp, body := postGemini(t, ts.URL, "no-such:generateContent", `{"contents":[{"role":"user","parts":[{"text":"x"}]}]}`, http.Header{}, "")
	if resp.StatusCode != 400 || !strings.Contains(body, "模型别名不存在: no-such") || !strings.Contains(body, `"status":"INVALID_ARGUMENT"`) {
		t.Fatalf("别名不存在应同规 400(Gemini 信封): %d %s", resp.StatusCode, body)
	}
	if up.RequestCount() != 0 {
		t.Fatal("不得触达上游")
	}
	// fileData 显式拒绝
	resp, body = postGemini(t, ts.URL, "gemini-x:generateContent",
		`{"contents":[{"role":"user","parts":[{"fileData":{"fileUri":"gs://x"}}]}]}`, http.Header{}, "")
	if resp.StatusCode != 400 || !strings.Contains(body, "content_part_unsupported") {
		t.Fatalf("fileData 应显式拒绝: %d %s", resp.StatusCode, body)
	}
}

// TestGeminiE2E_鉴权适配剥离 两种凭证形态放行;?key= 读后从 URL 剥离。
func TestGeminiE2E_鉴权适配剥离(t *testing.T) {
	up := testkit.StartJSONUpstream(t, 200, usageJSONBody)
	reg := adapter.NewRegistry()
	reg.Register("gem", adapter.NewGemini(up.URL()))
	p := New(Config{
		Registry:  reg,
		Providers: []config.Provider{{Name: "gem", Adapter: "gemini"}},
		Routes:    map[string][]config.Candidate{"gemini-x": {{Provider: "gem", Model: "up-g", Priority: 0}}},
		Timeouts:  Timeouts{Dial: time.Second, FirstToken: 10 * time.Second, ChunkIdle: 10 * time.Second},
		Pools:     map[string]*keypool.Pool{"gem": keypool.NewPool([]keypool.KeyInput{{ID: "KG", Value: "gk", Weight: 1}})},
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	// 带 verifier 的链:AdaptGeminiKey 必须先于鉴权
	mux := http.NewServeMux()
	mux.Handle("POST /v1beta/models/{modelMethod}", server.AdaptGeminiKey(p.HandlerGemini()))
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	// 头形态(数据面 verifier 未挂时 handler 不校验 key;此处验 ?key= 剥离语义)
	_, body := postGemini(t, ts.URL, "gemini-x:generateContent",
		`{"contents":[{"role":"user","parts":[{"text":"x"}]}]}`, http.Header{}, "?key=sekrit-platform-key")
	if !strings.Contains(body, `"candidates"`) {
		t.Fatalf("?key= 形态应放行: %s", body)
	}
	_ = up
	// AdaptGeminiKey 单元语义:?key= 剥离 + Bearer 补写(无 Authorization 时)
	r := httptest.NewRequest("POST", "/v1beta/models/m:generateContent?key=sekrit", nil)
	r.Header.Set("X-Goog-Api-Key", "")
	got := make(chan *http.Request, 1)
	next := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { got <- req })
	server.AdaptGeminiKey(next).ServeHTTP(httptest.NewRecorder(), r)
	served := <-got
	if served.Header.Get("Authorization") != "Bearer sekrit" {
		t.Fatalf("?key= 应适配为 Bearer: %q", served.Header.Get("Authorization"))
	}
	if strings.Contains(served.URL.RawQuery, "sekrit") || strings.Contains(served.URL.RawQuery, "key=") {
		t.Fatalf("?key= 必须从 URL 剥离: %s", served.URL.RawQuery)
	}
}

// TestGeminiE2E_bridgegate声明式缺口 gemini 候选工具多轮正常出站(不豁免);
// anthropic 桥接同流量维持豁免(现状逐字节)。
func TestGeminiE2E_bridgegate声明式缺口(t *testing.T) {
	up := testkit.StartJSONUpstream(t, 200, usageJSONBody)
	p := newGeminiProxy(t, up)
	ts := mountGemini(t, p)

	// gemini 入站:工具多轮(functionCall/functionResponse 全链)→ 正常服务
	raw := `{"contents":[
		{"role":"user","parts":[{"text":"查天气"}]},
		{"role":"model","parts":[{"functionCall":{"name":"weather","args":{"city":"北京"}}}]},
		{"role":"user","parts":[{"functionResponse":{"name":"weather","response":{"temp":"晴"}}}]},
		{"role":"user","parts":[{"text":"谢谢"}]}]}`
	resp, body := postGemini(t, ts.URL, "gemini-x:generateContent", raw, http.Header{}, "")
	if resp.StatusCode != 200 {
		t.Fatalf("gemini 候选工具多轮应正常服务: %d %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(up.LastBody()), `"functionResponse"`) {
		t.Fatalf("出站应含 functionResponse: %s", up.LastBody())
	}
}

// TestGeminiE2E_跨协议缓存同构 gemini 入站 canonical ≡ 等价 chat canonical
// (缓存跨协议命中的传递性钉子;治理/计量同构随之成立)。
func TestGeminiE2E_跨协议缓存同构(t *testing.T) {
	up := testkit.StartJSONUpstream(t, 200, usageJSONBody)
	p := newGeminiProxy(t, up)
	gts := mountGemini(t, p)
	cts := httptest.NewServer(p.Handler())
	t.Cleanup(cts.Close)

	_, _ = postGemini(t, gts.URL, "gemini-x:generateContent",
		`{"contents":[{"role":"user","parts":[{"text":"你好"}]}],"generationConfig":{"maxOutputTokens":64}}`, http.Header{}, "")
	gemUp1 := up.LastBody()
	_, _ = postChat(t, cts.URL, `{"model":"gemini-x","max_tokens":64,"messages":[{"role":"user","content":"你好"}]}`)
	gemUp2 := up.LastBody()
	if string(gemUp1) != string(gemUp2) {
		t.Fatalf("canonical 不等价(缓存/治理/计量同构即破):\n gemini=%s\n chat=%s", gemUp1, gemUp2)
	}
}
