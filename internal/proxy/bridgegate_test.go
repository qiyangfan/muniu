// 验证桥接适配器工具多轮能力不足时的换道行为和 Anthropic 翻译路径。
package proxy

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/solvela/muniu/internal/adapter"
	"github.com/solvela/muniu/internal/adapter/bridge"
	"github.com/solvela/muniu/internal/alerting"
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

// 多候选别名：工具多轮流量跳过桥接候选，经 openai 候选服务成功，客户端无感。
func TestBridgeGate_多候选豁免换道(t *testing.T) {
	up := testkit.StartJSONUpstream(t, 200, testkit.JSONOKBody)
	reg := adapter.NewRegistry()
	reg.Register("openai-p", adapter.NewOpenAI(up.URL(), ""))
	reg.Register("anthropic-p", testkit.NewFakeIncompleteBridge("http://127.0.0.1:1"))
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
		RandIntn: func(n int) int { return 0 }, // 固定从优先级更高的桥接候选开始
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ts := httptest.NewServer(p.Handler())
	t.Cleanup(ts.Close)

	toolBody := `{"model":"alias-a","messages":[{"role":"user","content":"hi"},{"role":"tool","tool_call_id":"call_1","content":"res"}]}`
	resp, body := postChat(t, ts.URL, toolBody)
	if resp.StatusCode != 200 {
		t.Fatalf("应经 openai 候选服务成功: %d %s", resp.StatusCode, body)
	}
	if strings.Contains(body, "bridge_tool") {
		t.Fatalf("多候选豁免不应暴露边界: %s", body)
	}
}

// 桥接独占别名：返回 400 专用信封而非 upstream_error，且零上游调用。
func TestBridgeGate_独占别名明确信封(t *testing.T) {
	up := testkit.StartJSONUpstream(t, 200, testkit.JSONOKBody) // 占位，不应被调用
	_ = up
	reg := adapter.NewRegistry()
	reg.Register("anthropic-p", testkit.NewFakeIncompleteBridge("http://127.0.0.1:1"))
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

	toolBody := `{"model":"alias-a","messages":[{"role":"user","content":"hi"},{"role":"tool","tool_call_id":"call_1","content":"res"}]}`
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

// 豁免不计 EMA/冷却：桥接独占别名连续豁免后，key 池健康度零变化。
func TestBridgeGate_豁免无健康度副作用(t *testing.T) {
	reg := adapter.NewRegistry()
	reg.Register("anthropic-p", testkit.NewFakeIncompleteBridge("http://127.0.0.1:1"))
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

// anthropic 声明翻译完整后，chat 工具多轮独占别名经翻译路径正常出站：
// 上游收到 tool_use 块形态请求，客户端收到响应侧 tool_calls 翻译，不再豁免或拒绝。
func TestBridgeGate_anthropic翻正后工具多轮出站(t *testing.T) {
	// 上游返回 anthropic 形态响应，含 tool_use 块。
	anthropicBody := `{"id":"msg_1","type":"message","role":"assistant","model":"claude-x",` +
		`"content":[{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{"city":"北京"}}],` +
		`"stop_reason":"tool_use","usage":{"input_tokens":21,"output_tokens":5}}`
	up := testkit.StartJSONUpstream(t, 200, anthropicBody)
	reg := adapter.NewRegistry()
	reg.Register("anthropic-p", bridge.NewAnthropic(up.URL(), adapter.Options{}))
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

	toolBody := `{"model":"alias-a","max_tokens":256,
		"messages":[
			{"role":"user","content":"北京天气"},
			{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"北京\"}"}}]},
			{"role":"tool","tool_call_id":"call_1","content":"晴 25°C"}
		],
		"tools":[{"type":"function","function":{"name":"get_weather","parameters":{"type":"object"}}}]}`
	resp, body := postChat(t, ts.URL, toolBody)
	if resp.StatusCode != 200 {
		t.Fatalf("翻正后应经翻译路径出站成功: %d %s", resp.StatusCode, body)
	}
	// 出站 wire 为 anthropic 形态：assistant tool_use 块 + user tool_result 块。
	wire := string(up.LastBody())
	if !strings.Contains(wire, `"type":"tool_use"`) || !strings.Contains(wire, `"name":"get_weather"`) ||
		!strings.Contains(wire, `{"city":"北京"}`) || !strings.Contains(wire, `"type":"tool_result"`) ||
		!strings.Contains(wire, `"tool_use_id":"call_1"`) {
		t.Fatalf("出站应为 anthropic 工具块形态: %s", wire)
	}
	// 响应侧既有能力：tool_use → chat tool_calls 翻译。
	if !strings.Contains(body, `"tool_calls"`) || !strings.Contains(body, "get_weather") {
		t.Fatalf("响应应为 tool_calls 翻译形态: %s", body)
	}
}

// 首轮 tools 声明不含中途标记，经桥接正常出站，不受边界影响。
func TestBridgeGate_首轮tools声明放行(t *testing.T) {
	// 桥接 Prepare 对 tools 的翻译不需要真上游可达，这里只验证不被豁免：
	// 用 httptest 起一个伪 anthropic，返回 500 即可证明请求确实出站。
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

// TestAlerting_候选全失败通知 验证候选耗尽时发送告警，告警失败不影响请求。
func TestAlerting_候选全失败通知(t *testing.T) {
	var hits atomic.Int64
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError) // webhook 故障，请求必须照常
	}))
	t.Cleanup(hook.Close)
	reg := adapter.NewRegistry()
	reg.Register("dead-p", adapter.NewOpenAI("http://127.0.0.1:1", ""))
	p := New(Config{
		Registry: reg,
		Alerts:   alerting.New(alerting.Options{WebhookURL: hook.URL, Backoff: []time.Duration{time.Millisecond}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}),
		Routes: map[string][]config.Candidate{
			"alias-a": {{Provider: "dead-p", Model: "m", Priority: 0}},
		},
		Timeouts: Timeouts{Dial: time.Second, FirstToken: 10 * time.Second, ChunkIdle: 10 * time.Second},
		Pools:    map[string]*keypool.Pool{"dead-p": keypool.NewPool([]keypool.KeyInput{{ID: "K", Value: "k", Weight: 1}})},
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ts := httptest.NewServer(p.Handler())
	t.Cleanup(ts.Close)

	resp, body := postChat(t, ts.URL, `{"model":"alias-a","messages":[{"role":"user","content":"hi"}]}`)
	if resp.StatusCode == 0 || body == "" {
		t.Fatal("请求必须有响应")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && hits.Load() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if hits.Load() == 0 {
		t.Fatal("候选全失败应触发告警通知")
	}
}
