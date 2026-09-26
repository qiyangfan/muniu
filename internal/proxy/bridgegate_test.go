// 桥接工具多轮显式边界契约测试(upstream-egress-batch):
// 检测纯函数表驱动、多候选换道豁免(客户端无感)、桥接独占明确信封、
// 豁免不计 EMA/冷却、首轮 tools 声明放行。
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
	"github.com/solvela/muniu/internal/adapter/bridge"
	"github.com/solvela/muniu/internal/config"
	"github.com/solvela/muniu/internal/keypool"
	"github.com/solvela/muniu/internal/protocol"
	"github.com/solvela/muniu/internal/testkit"
)

func TestChatToolLoopInflight_表驱动(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"纯对话", `{"model":"m","messages":[{"role":"user","content":"hi"}]}`, false},
		{"tools 声明无中途标记", `{"model":"m","tools":[{"type":"function"}],"messages":[{"role":"user","content":"hi"}]}`, false},
		{"role_tool", `{"model":"m","messages":[{"role":"user","content":"hi"},{"role":"tool","content":"res"}]}`, true},
		{"assistant_tool_calls", `{"model":"m","messages":[{"role":"assistant","tool_calls":[{"id":"c1"}]}]}`, true},
		{"tool_calls_null", `{"model":"m","messages":[{"role":"assistant","tool_calls":null}]}`, false},
		{"tool_calls空数组", `{"model":"m","messages":[{"role":"assistant","tool_calls":[]}]}`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req, err := protocol.ParseChatRequest([]byte(c.body))
			if err != nil {
				t.Fatal(err)
			}
			if got := chatToolLoopInflight(req); got != c.want {
				t.Fatalf("chatToolLoopInflight = %v, 期望 %v", got, c.want)
			}
		})
	}
}

// 多候选别名:工具多轮流量跳过桥接候选,经 openai 候选服务成功(客户端无感)。
func TestBridgeGate_多候选豁免换道(t *testing.T) {
	up := testkit.StartJSONUpstream(t, 200, testkit.JSONOKBody)
	reg := adapter.NewRegistry()
	reg.Register("openai-p", adapter.NewOpenAI(up.URL(), ""))
	reg.Register("anthropic-p", bridge.NewAnthropic("http://127.0.0.1:1", adapter.Options{}))
	p := New(Config{
		Registry: reg,
		Routes: map[string][]config.Candidate{
			"alias-a": {
				{Provider: "anthropic-p", Model: "claude-x", Priority: 0},
				{Provider: "openai-p", Model: "up-a", Priority: 1},
			},
		},
		Timeouts: Timeouts{Dial: time.Second, FirstToken: 10 * time.Second, ChunkIdle: 10 * time.Second},
		Pools: map[string]*keypool.Pool{
			"openai-p":    keypool.NewPool([]keypool.KeyInput{{ID: "K1", Value: "k", Weight: 1}}),
			"anthropic-p": keypool.NewPool([]keypool.KeyInput{{ID: "K2", Value: "k", Weight: 1}}),
		},
		RandIntn: func(n int) int { return 0 }, // 固定从桥接候选(优先级更高)开始
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ts := httptest.NewServer(p.Handler())
	t.Cleanup(ts.Close)

	toolBody := `{"model":"alias-a","messages":[{"role":"user","content":"hi"},{"role":"tool","content":"res"}]}`
	resp, body := postChat(t, ts.URL, toolBody)
	if resp.StatusCode != 200 {
		t.Fatalf("应经 openai 候选服务成功: %d %s", resp.StatusCode, body)
	}
	if strings.Contains(body, "bridge_tool") {
		t.Fatalf("多候选豁免不应暴露边界: %s", body)
	}
}

// 桥接独占别名:400 专用信封(而非上游 400 的 upstream_error),且零上游调用。
func TestBridgeGate_独占别名明确信封(t *testing.T) {
	up := testkit.StartJSONUpstream(t, 200, testkit.JSONOKBody) // 占位:不应被调用
	_ = up
	reg := adapter.NewRegistry()
	reg.Register("anthropic-p", bridge.NewAnthropic("http://127.0.0.1:1", adapter.Options{}))
	p := New(Config{
		Registry: reg,
		Routes: map[string][]config.Candidate{
			"alias-a": {{Provider: "anthropic-p", Model: "claude-x", Priority: 0}},
		},
		Timeouts: Timeouts{Dial: time.Second, FirstToken: 10 * time.Second, ChunkIdle: 10 * time.Second},
		Pools: map[string]*keypool.Pool{
			"anthropic-p": keypool.NewPool([]keypool.KeyInput{{ID: "K2", Value: "k", Weight: 1}}),
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ts := httptest.NewServer(p.Handler())
	t.Cleanup(ts.Close)

	toolBody := `{"model":"alias-a","messages":[{"role":"user","content":"hi"},{"role":"tool","content":"res"}]}`
	resp, body := postChat(t, ts.URL, toolBody)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("应 400: %d %s", resp.StatusCode, body)
	}
	var env struct {
		Error struct {
			Type    string `json:"type"`
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatal(err)
	}
	if env.Error.Code != "bridge_tool_roundtrip_unsupported" || env.Error.Type != "bad_request" {
		t.Fatalf("信封不符: %+v", env.Error)
	}
}

// 豁免不计 EMA/冷却:桥接独占别名连续豁免后,key 池健康度零变化。
func TestBridgeGate_豁免无健康度副作用(t *testing.T) {
	reg := adapter.NewRegistry()
	ad := bridge.NewAnthropic("http://127.0.0.1:1", adapter.Options{})
	reg.Register("anthropic-p", ad)
	pool := keypool.NewPool([]keypool.KeyInput{{ID: "K2", Value: "k", Weight: 1}})
	p := New(Config{
		Registry: reg,
		Routes: map[string][]config.Candidate{
			"alias-a": {{Provider: "anthropic-p", Model: "claude-x", Priority: 0}},
		},
		Timeouts: Timeouts{Dial: time.Second, FirstToken: 10 * time.Second, ChunkIdle: 10 * time.Second},
		Pools:    map[string]*keypool.Pool{"anthropic-p": pool},
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ts := httptest.NewServer(p.Handler())
	t.Cleanup(ts.Close)
	for i := 0; i < 5; i++ {
		toolBody := `{"model":"alias-a","messages":[{"role":"tool","content":"res"}]}`
		resp, _ := postChat(t, ts.URL, toolBody)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("第 %d 次应 400: %d", i, resp.StatusCode)
		}
	}
	if st := pool.StateOf("K2", time.Now()); st != "healthy" {
		t.Fatalf("豁免不得影响 key 健康度,实际 %s", st)
	}
}

// 首轮 tools 声明(无中途标记)经桥接正常出站,不受边界影响。
func TestBridgeGate_首轮tools声明放行(t *testing.T) {
	// 桥接 Prepare 对 tools 的翻译不需要真上游可达——这里只验证「不被豁免」:
	// 用httptest 起一个伪 anthropic(simple 500 也会先证明请求确实出站了)
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)
	reg := adapter.NewRegistry()
	reg.Register("anthropic-p", bridge.NewAnthropic(srv.URL, adapter.Options{}))
	p := New(Config{
		Registry: reg,
		Routes: map[string][]config.Candidate{
			"alias-a": {{Provider: "anthropic-p", Model: "claude-x", Priority: 0}},
		},
		Timeouts: Timeouts{Dial: time.Second, FirstToken: 10 * time.Second, ChunkIdle: 10 * time.Second},
		Pools:    map[string]*keypool.Pool{"anthropic-p": keypool.NewPool([]keypool.KeyInput{{ID: "K2", Value: "k", Weight: 1}})},
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ts := httptest.NewServer(p.Handler())
	t.Cleanup(ts.Close)
	toolsBody := `{"model":"alias-a","tools":[{"type":"function","function":{"name":"f"}}],"messages":[{"role":"user","content":"hi"}]}`
	postChat(t, ts.URL, toolsBody)
	if !called {
		t.Fatal("首轮 tools 声明应照常出站(不被豁免)")
	}
}
