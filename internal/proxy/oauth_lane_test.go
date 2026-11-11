// OAuth 订阅 lane 端到端(假上游):chat/messages 可达 oauth 渠道(出站头形态断言),
// gemini/embeddings 收窄(全 oauth 别名显式信封、混合候选零上游跳过)、
// 亲和 worth 闸门在 oauth 渠道照常建粘。lane handler 按 cmd 装配同形态逐 lane 挂载。
package proxy

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/solvela/muniu/internal/adapter"
	_ "github.com/solvela/muniu/internal/adapter/bridge" // 接线 anthropic 桥接工厂
	"github.com/solvela/muniu/internal/config"
	"github.com/solvela/muniu/internal/keypool"
	"github.com/solvela/muniu/internal/protocol"
	"github.com/solvela/muniu/internal/testkit"
)

// anthropicMessageJSON 最小 Anthropic Messages 响应(含 cache_read 供亲和 worth 证据)。
const anthropicMessageJSON = `{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":5,"cache_read_input_tokens":64,"output_tokens":2}}`

const messagesBody = `{"model":"alias-co","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`

// newOAuthProxy 单 oauth 渠道(anthropic 桥接 + auth=oauth)+普通 openai 渠道,二者
// 共用同一假上游;routes 按用例声明。返回 proxy 与其 oauth 渠道上游记录器。
func newOAuthProxy(t *testing.T, routes map[string][]config.Candidate) (*Proxy, *httptest.Server, *testkit.Upstream) {
	t.Helper()
	up := testkit.StartJSONUpstream(t, 200, anthropicMessageJSON)
	reg := adapter.NewRegistry()
	// oauth 渠道 adapter:auth 形态声明进适配器(头分支按此);凭据经池成员注入
	co, err := adapter.For("anthropic", up.URL(), "ignored", adapter.Options{Auth: adapter.AuthOAuth})
	if err != nil {
		t.Fatalf("构造 oauth 适配器: %v", err)
	}
	reg.Register("co", co)
	reg.Register("po", adapter.NewOpenAI(up.URL(), "sk-po"))
	p := New(Config{
		Registry: reg,
		Providers: []config.Provider{
			{Name: "co", Adapter: "anthropic", BaseURL: up.URL(), Auth: adapter.AuthOAuth},
			{Name: "po", Adapter: "openai", BaseURL: up.URL()},
		},
		Routes:   routes,
		Timeouts: defTimeouts(),
		Pools: map[string]*keypool.Pool{
			"co": keypool.NewPool([]keypool.KeyInput{{ID: "acc-1", Value: "at-token-1", Weight: 1}}),
			"po": keypool.NewPool([]keypool.KeyInput{{ID: "K-po", Value: "sk-po", Weight: 1}}),
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ts := httptest.NewServer(p.Handler())
	t.Cleanup(ts.Close)
	return p, ts, up
}

func TestOAuthLane_Chat经oauth渠道头形态(t *testing.T) {
	_, ts, up := newOAuthProxy(t, map[string][]config.Candidate{
		"alias-co": {{Provider: "co", Model: "claude-sonnet-4-5", Priority: 0}},
	})
	resp, body := postChat(t, ts.URL, `{"model":"alias-co","messages":[{"role":"user","content":"hi"}]}`)
	if resp.StatusCode != 200 {
		t.Fatalf("chat 经 oauth 渠道应 200: %d %s", resp.StatusCode, body)
	}
	if got := up.LastHeader("Authorization"); got != "Bearer at-token-1" {
		t.Fatalf("oauth 渠道出站应 Bearer access token, got %q", got)
	}
	if got := up.LastHeader("anthropic-beta"); got != "oauth-2025-04-20" {
		t.Fatalf("oauth 渠道出站应带官方 beta 头, got %q", got)
	}
	if got := up.LastHeader("x-api-key"); got != "" {
		t.Fatalf("oauth 渠道不应发 x-api-key, got %q", got)
	}
	// UA 零覆写(保号边界):仅传输层默认 UA,不得出现伪装的 CLI 客户端指纹
	if got := up.LastHeader("User-Agent"); strings.Contains(strings.ToLower(got), "claude") {
		t.Fatalf("UA 不得伪装 CLI 指纹(缺省零覆写), got %q", got)
	}
}

func TestOAuthLane_Messages直通oauth渠道(t *testing.T) {
	p, _, up := newOAuthProxy(t, map[string][]config.Candidate{
		"alias-co": {{Provider: "co", Model: "claude-sonnet-4-5", Priority: 0}},
	})
	ts := httptest.NewServer(p.HandlerMessages())
	t.Cleanup(ts.Close)
	req, _ := http.NewRequest("POST", ts.URL+"/v1/messages", strings.NewReader(messagesBody))
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 {
		t.Fatalf("messages 直通应 200: %d %s", res.StatusCode, b)
	}
	if !strings.Contains(string(b), `"type":"message"`) {
		t.Fatalf("messages 直通应原样返回 anthropic 形态: %s", b)
	}
	if got := up.LastHeader("Authorization"); got != "Bearer at-token-1" {
		t.Fatalf("messages 直通 oauth 头失真: %q", got)
	}
}

func TestOAuthLane_全oauth别名embeddings显式拒绝(t *testing.T) {
	p, _, up := newOAuthProxy(t, map[string][]config.Candidate{
		"emb-oauth": {{Provider: "co", Model: "emb", Priority: 0}},
	})
	mux := http.NewServeMux()
	mux.Handle("POST /v1/embeddings", p.HandlerEmbeddings())
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	req, _ := http.NewRequest("POST", ts.URL+"/v1/embeddings", strings.NewReader(`{"model":"emb-oauth","input":"hi"}`))
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode != 400 || !strings.Contains(string(b), oauthLaneUnsupportedCode) {
		t.Fatalf("全 oauth 别名 embeddings 应 400 显式信封: %d %s", res.StatusCode, b)
	}
	if up.RequestCount() != 0 {
		t.Fatalf("收窄必须上游零调用, 实际 %d", up.RequestCount())
	}
}

func TestOAuthLane_混合候选oauth跳过由非oauth服务(t *testing.T) {
	upPo := testkit.StartJSONUpstream(t, 200, `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2],"usage":{"prompt_tokens":1,"total_tokens":1}}]}`)
	reg := adapter.NewRegistry()
	co, _ := adapter.For("anthropic", upPo.URL(), "x", adapter.Options{Auth: adapter.AuthOAuth})
	reg.Register("co", co)
	reg.Register("po", adapter.NewOpenAI(upPo.URL(), "sk"))
	p := New(Config{
		Registry: reg,
		Providers: []config.Provider{
			{Name: "co", Adapter: "anthropic", BaseURL: upPo.URL(), Auth: adapter.AuthOAuth},
			{Name: "po", Adapter: "openai", BaseURL: upPo.URL()},
		},
		Routes: map[string][]config.Candidate{
			"emb-mixed": {
				{Provider: "co", Model: "emb", Priority: 0},
				{Provider: "po", Model: "emb", Priority: 1},
			},
		},
		Timeouts: defTimeouts(),
		Pools: map[string]*keypool.Pool{
			"co": keypool.NewPool([]keypool.KeyInput{{ID: "acc-1", Value: "at", Weight: 1}}),
			"po": keypool.NewPool([]keypool.KeyInput{{ID: "K-po", Value: "sk", Weight: 1}}),
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	mux := http.NewServeMux()
	mux.Handle("POST /v1/embeddings", p.HandlerEmbeddings())
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	req, _ := http.NewRequest("POST", ts.URL+"/v1/embeddings", strings.NewReader(`{"model":"emb-mixed","input":"hi"}`))
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 || !strings.Contains(string(b), "embedding") {
		t.Fatalf("混合候选应由非 oauth 候选服务: %d %s", res.StatusCode, b)
	}
	if upPo.RequestCount() != 1 {
		t.Fatalf("oauth 候选必须零上游调用(仅 po 一次): %d", upPo.RequestCount())
	}
}

func TestOAuthLane_混合跳因走通用信封(t *testing.T) {
	// gemini 入站:oauth 收窄与池耗尽并存时,耗尽分支必须走通用信封(!otherSkipped 守卫)
	// ——oauth 信封冒认唯一跳因会掩盖「非 oauth 候选不可用」的真实故障面
	up := testkit.StartJSONUpstream(t, 200, anthropicMessageJSON)
	reg := adapter.NewRegistry()
	co, err := adapter.For("anthropic", up.URL(), "ignored", adapter.Options{Auth: adapter.AuthOAuth})
	if err != nil {
		t.Fatalf("构造 oauth 适配器: %v", err)
	}
	reg.Register("co", co)
	reg.Register("po", adapter.NewOpenAI(up.URL(), "sk-po"))
	p := New(Config{
		Registry: reg,
		Providers: []config.Provider{
			{Name: "co", Adapter: "anthropic", BaseURL: up.URL(), Auth: adapter.AuthOAuth},
			{Name: "po", Adapter: "openai", BaseURL: up.URL()},
		},
		Routes: map[string][]config.Candidate{
			"alias-mixed": {
				{Provider: "co", Model: "claude-sonnet-4-5", Priority: 0},
				{Provider: "po", Model: "claude-sonnet-4-5", Priority: 1},
			},
		},
		Timeouts: defTimeouts(),
		Pools: map[string]*keypool.Pool{
			"co": keypool.NewPool([]keypool.KeyInput{{ID: "acc-1", Value: "at", Weight: 1}}),
			"po": keypool.NewPool(nil), // 全耗尽:Pick 恒 nil → otherSkipped
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	mux := http.NewServeMux()
	mux.Handle("POST /v1beta/models/{modelMethod}", p.HandlerGemini())
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	req, _ := http.NewRequest("POST", ts.URL+"/v1beta/models/alias-mixed:generateContent",
		strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`))
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	b, _ := io.ReadAll(res.Body)
	// gemini 信封形态无 muniu code 字段:通用信封以 message 文本判定,oauth 信封文本缺席
	if res.StatusCode != 502 || !strings.Contains(string(b), "已尝试全部候选") {
		t.Fatalf("混合跳因应走通用信封: %d %s", res.StatusCode, b)
	}
	if strings.Contains(string(b), "不支持当前入站 lane") {
		t.Fatalf("oauth 信封不得冒认唯一跳因: %s", b)
	}
	if up.RequestCount() != 0 {
		t.Fatalf("两候选均不可服务必须上游零调用: %d", up.RequestCount())
	}
}

func TestOAuthLane_Gemini入站oauth收窄(t *testing.T) {
	p, _, up := newOAuthProxy(t, map[string][]config.Candidate{
		"alias-co": {{Provider: "co", Model: "claude-sonnet-4-5", Priority: 0}},
	})
	mux := http.NewServeMux()
	mux.Handle("POST /v1beta/models/{modelMethod}", p.HandlerGemini())
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	req, _ := http.NewRequest("POST", ts.URL+"/v1beta/models/alias-co:generateContent",
		strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`))
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	b, _ := io.ReadAll(res.Body)
	// gemini 信封形态无 muniu code 字段,显式性由 message 文本承载
	if res.StatusCode != 400 || !strings.Contains(string(b), "不支持当前入站 lane") {
		t.Fatalf("gemini 入站全 oauth 候选应显式拒绝: %d %s", res.StatusCode, b)
	}
	if up.RequestCount() != 0 {
		t.Fatalf("gemini 收窄必须上游零调用: %d", up.RequestCount())
	}
}

func TestOAuthLane_亲和worth在oauth渠道建粘(t *testing.T) {
	// usage 携带 cache_read(anthropic 方言 cache_read_input_tokens)→ worth 证据成立 →
	// 亲和表建粘;oauth 渠道不改变亲和语义(ADR-0020 复用零特例)
	p, ts, _ := newOAuthProxy(t, map[string][]config.Candidate{
		"alias-co": {{Provider: "co", Model: "claude-sonnet-4-5", Priority: 0}},
	})
	if resp, body := postChat(t, ts.URL, `{"model":"alias-co","messages":[{"role":"user","content":"hi"}]}`); resp.StatusCode != 200 {
		t.Fatalf("chat 应 200: %s", body)
	}
	// chat 亲和身份 = apiKeyID+首条 user 哈希(无头无 key 场景为确定性哈希),直接断言表内 co 条目
	found := false
	p.cacheAffinity.mu.Lock()
	for _, el := range p.cacheAffinity.entries {
		if en := el.Value.(*cacheAffinityEntry); en.provider == "co" && en.keyID == "acc-1" {
			found = true
		}
	}
	p.cacheAffinity.mu.Unlock()
	if !found {
		t.Fatal("oauth 渠道 cache 读证据应照常建立亲和条目(零特例)")
	}
}

func TestAffinityLane_Gemini入站漂移根修回归(t *testing.T) {
	// ADR-0020 声明 gemini 零触碰,但 gemini 委托管道不携带任何身份曾被误当 chat 参与
	// 亲和(nat==nil)。lane 标记根修后:cache 读证据成立的 gemini 请求不得建粘。
	// 突变敏感:移除 affinityLane 的 laneFrom 判定必红。
	up := testkit.StartJSONUpstream(t, 200, `{"id":"x","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2,"prompt_tokens_details":{"cached_tokens":64}}}`)
	reg := adapter.NewRegistry()
	reg.Register("po", adapter.NewOpenAI(up.URL(), "sk"))
	p := New(Config{
		Registry: reg,
		Providers: []config.Provider{
			{Name: "po", Adapter: "openai", BaseURL: up.URL()},
		},
		Routes: map[string][]config.Candidate{
			"alias-po": {{Provider: "po", Model: "gem-claude", Priority: 0}},
		},
		Timeouts: defTimeouts(),
		Pools: map[string]*keypool.Pool{
			"po": keypool.NewPool([]keypool.KeyInput{{ID: "K-po", Value: "sk", Weight: 1}}),
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	mux := http.NewServeMux()
	mux.Handle("POST /v1beta/models/{modelMethod}", p.HandlerGemini())
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	req, _ := http.NewRequest("POST", ts.URL+"/v1beta/models/alias-po:generateContent",
		strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`))
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != 200 {
		t.Fatalf("gemini 请求应 200: %d", res.StatusCode)
	}
	p.cacheAffinity.mu.Lock()
	n := len(p.cacheAffinity.entries)
	p.cacheAffinity.mu.Unlock()
	if n != 0 {
		t.Fatalf("gemini 入站不得建立亲和条目(零触碰声明), 实际 %d 条", n)
	}
}

// TestOauthSkippedLane_判定表 nat/lane 双通道判定逐一钉住(评审域一测试缺口:
// 移除 nat 分支的突变曾可存活全套件——gemini 用例只覆盖 nat==nil 路径)。
func TestOauthSkippedLane_判定表(t *testing.T) {
	oauthModes := map[string]string{"co": adapter.AuthOAuth}
	plainModes := map[string]string{"po": ""}
	mkReq := func(lane protocol.InboundProtocol) *http.Request {
		r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
		if lane != "" {
			r = withLane(r, lane)
		}
		return r
	}
	cases := []struct {
		name  string
		req   *http.Request
		nat   *inboundNative
		modes map[string]string
		prov  string
		want  bool
	}{
		{name: "chat入站_nat空_无标记_放行", req: mkReq(""), nat: nil, modes: oauthModes, prov: "co", want: false},
		{name: "messages入站_natmessages_放行", req: mkReq(""), nat: &inboundNative{proto: protocol.InboundMessages}, modes: oauthModes, prov: "co", want: false},
		{name: "responses入站_拒", req: mkReq(""), nat: &inboundNative{proto: protocol.InboundResponses}, modes: oauthModes, prov: "co", want: true},
		{name: "gemini入站_经lane标记_拒", req: mkReq(protocol.InboundGemini), nat: nil, modes: oauthModes, prov: "co", want: true},
		{name: "非oauth渠道_恒放行", req: mkReq(protocol.InboundGemini), nat: nil, modes: plainModes, prov: "po", want: false},
		{name: "非oauth渠道_natresponses_放行", req: mkReq(""), nat: &inboundNative{proto: protocol.InboundResponses}, modes: plainModes, prov: "po", want: false},
	}
	for _, tc := range cases {
		if got := oauthSkippedLane(tc.req, tc.nat, tc.modes, tc.prov); got != tc.want {
			t.Fatalf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}
