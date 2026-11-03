// outbound-proxy 契约测试:渠道级代理声明的路由行为——声明渠道必经代理,零声明渠道与
// 默认 client 同实例(env 语义不变),热替换随拓扑重建。
package proxy

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sync"
	"testing"

	"github.com/solvela/muniu/internal/adapter"
	"github.com/solvela/muniu/internal/config"
	"github.com/solvela/muniu/internal/keypool"
	"github.com/solvela/muniu/internal/testkit"
)

// recordingProxy 伪代理:记录每次代理的目标 host,并把绝对 URI 请求原样转发到目标上游
// (http 上游经代理走绝对 URI 形态,无需 CONNECT 隧道即可证明「经代理」)。
type recordingProxy struct {
	*httptest.Server
	mu      sync.Mutex
	targets []string
}

func newRecordingProxy(t *testing.T) *recordingProxy {
	t.Helper()
	rp := &recordingProxy{}
	rp.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rp.mu.Lock()
		rp.targets = append(rp.targets, r.URL.Host)
		rp.mu.Unlock()
		req, err := http.NewRequest(r.Method, r.URL.String(), r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		resp, err := http.DefaultTransport.RoundTrip(req)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		defer func() { _ = resp.Body.Close() }()
		for k, vs := range resp.Header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	t.Cleanup(rp.Close)
	return rp
}

func (rp *recordingProxy) hosts() []string {
	rp.mu.Lock()
	defer rp.mu.Unlock()
	return append([]string(nil), rp.targets...)
}

func newDualProviderProxy(t *testing.T, upstreamA, upstreamB, providerAProxy string) (*Proxy, *httptest.Server) {
	t.Helper()
	reg := adapter.NewRegistry()
	reg.Register("pa", adapter.NewOpenAI(upstreamA, "sk-a"))
	reg.Register("pb", adapter.NewOpenAI(upstreamB, "sk-b"))
	p := New(Config{
		Registry: reg,
		Providers: []config.Provider{
			{Name: "pa", Adapter: "openai", BaseURL: upstreamA, ProxyURL: providerAProxy},
			{Name: "pb", Adapter: "openai", BaseURL: upstreamB},
		},
		Routes: map[string][]config.Candidate{
			"alias-a": {
				{Provider: "pa", Model: "m", Priority: 0},
				{Provider: "pb", Model: "m", Priority: 1},
			},
		},
		Timeouts: defTimeouts(),
		Pools: map[string]*keypool.Pool{
			"pa": keypool.NewPool([]keypool.KeyInput{{ID: "KA", Value: "sk-a", Weight: 1}}),
			"pb": keypool.NewPool([]keypool.KeyInput{{ID: "KB", Value: "sk-b", Weight: 1}}),
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ts := httptest.NewServer(p.Handler())
	t.Cleanup(ts.Close)
	return p, ts
}

func TestOutboundProxy_声明渠道必经代理(t *testing.T) {
	upA := testkit.StartStreamUpstream(t, testkit.NormalEvents())
	upB := testkit.StartJSONUpstream(t, 200, "{}")
	rp := newRecordingProxy(t)

	_, ts := newDualProviderProxy(t, upA.URL(), upB.URL(), rp.URL)
	resp, body := postChat(t, ts.URL, streamChatBody)
	if resp.StatusCode != 200 {
		t.Fatalf("状态码 %d: %s", resp.StatusCode, body)
	}
	want, err := url.Parse(upA.URL())
	if err != nil {
		t.Fatal(err)
	}
	hosts := rp.hosts()
	if len(hosts) != 1 || hosts[0] != want.Host {
		t.Fatalf("声明渠道应经代理出站一次且目标正确: got %v want [%s]", hosts, want.Host)
	}
}

func TestOutboundProxy_零声明渠道与默认client同实例(t *testing.T) {
	upA := testkit.StartJSONUpstream(t, 200, chatCompletionJSON)
	upB := testkit.StartJSONUpstream(t, 200, chatCompletionJSON)
	rp := newRecordingProxy(t)

	p, ts := newDualProviderProxy(t, upA.URL(), upB.URL(), rp.URL)
	snap := p.topo.Load()
	if got := p.clientFor(snap, "pb"); got != p.client {
		t.Fatal("零声明渠道必须复用默认 client(env 语义不变)")
	}
	if cl := p.clientFor(snap, "pa"); cl == p.client {
		t.Fatal("声明渠道必须使用独立 client")
	}
	resp, body := postChat(t, ts.URL, chatBody)
	if resp.StatusCode != 200 {
		t.Fatalf("状态码 %d: %s", resp.StatusCode, body)
	}
	if n := len(rp.hosts()); n == 0 {
		t.Fatal("非流式路径声明渠道同样必须经代理")
	}
}

func TestOutboundProxy_热替换随拓扑重建(t *testing.T) {
	upA := testkit.StartJSONUpstream(t, 200, chatCompletionJSON)
	rp1 := newRecordingProxy(t)
	rp2 := newRecordingProxy(t)

	reg := adapter.NewRegistry()
	reg.Register("pa", adapter.NewOpenAI(upA.URL(), "sk-a"))
	p := New(Config{
		Registry:  reg,
		Providers: []config.Provider{{Name: "pa", Adapter: "openai", BaseURL: upA.URL(), ProxyURL: rp1.URL}},
		Routes:    map[string][]config.Candidate{"alias-a": {{Provider: "pa", Model: "m", Priority: 0}}},
		Timeouts:  defTimeouts(),
		Pools: map[string]*keypool.Pool{
			"pa": keypool.NewPool([]keypool.KeyInput{{ID: "KA", Value: "sk-a", Weight: 1}}),
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ts := httptest.NewServer(p.Handler())
	t.Cleanup(ts.Close)

	_, err := p.ReplaceTopology(Config{
		Providers: []config.Provider{{Name: "pa", Adapter: "openai", BaseURL: upA.URL(), Weight: 1, ProxyURL: rp2.URL}},
		Routes:    map[string][]config.Candidate{"alias-a": {{Provider: "pa", Model: "m", Priority: 0}}},
	}, func(prov config.Provider) (*keypool.Pool, adapter.Adapter, error) {
		ad, ok := reg.Get(prov.Name)
		if !ok {
			return nil, nil, http.ErrAbortHandler
		}
		return keypool.NewPool([]keypool.KeyInput{{ID: "KA", Value: "sk-a", Weight: 1}}), ad, nil
	})
	if err != nil {
		t.Fatalf("热替换被拒: %v", err)
	}
	resp, body := postChat(t, ts.URL, chatBody)
	if resp.StatusCode != 200 {
		t.Fatalf("状态码 %d: %s", resp.StatusCode, body)
	}
	if len(rp2.hosts()) == 0 {
		t.Fatal("热替换后声明渠道必须改经新代理出站")
	}
}

// chatCompletionJSON 非流式伪上游的最小合法响应,usage 齐备以过计量归一。
const chatCompletionJSON = `{"id":"x","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`

// TestOutboundProxy_仅代理声明变更_热替换生效 评审 A2 哨兵:全字段相等仅 ProxyURL 不同,
// 复用分支不得吞掉变更(旧实现 providersEqual 漏比较 ProxyURL,该用例在修复前必红)。
func TestOutboundProxy_仅代理声明变更_热替换生效(t *testing.T) {
	upA := testkit.StartJSONUpstream(t, 200, chatCompletionJSON)
	rp1 := newRecordingProxy(t)
	rp2 := newRecordingProxy(t)

	reg := adapter.NewRegistry()
	reg.Register("pa", adapter.NewOpenAI(upA.URL(), "sk-a"))
	p := New(Config{
		Registry:  reg,
		Providers: []config.Provider{{Name: "pa", Adapter: "openai", BaseURL: upA.URL(), Weight: 1, ProxyURL: rp1.URL}},
		Routes:    map[string][]config.Candidate{"alias-a": {{Provider: "pa", Model: "m", Priority: 0}}},
		Timeouts:  defTimeouts(),
		Pools: map[string]*keypool.Pool{
			"pa": keypool.NewPool([]keypool.KeyInput{{ID: "KA", Value: "sk-a", Weight: 1}}),
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ts := httptest.NewServer(p.Handler())
	t.Cleanup(ts.Close)

	if _, err := p.ReplaceTopology(Config{
		Providers: []config.Provider{{Name: "pa", Adapter: "openai", BaseURL: upA.URL(), Weight: 1, ProxyURL: rp2.URL}},
		Routes:    map[string][]config.Candidate{"alias-a": {{Provider: "pa", Model: "m", Priority: 0}}},
	}, func(prov config.Provider) (*keypool.Pool, adapter.Adapter, error) {
		ad, ok := reg.Get(prov.Name)
		if !ok {
			return nil, nil, fmt.Errorf("adapter 缺失")
		}
		return keypool.NewPool([]keypool.KeyInput{{ID: "KA", Value: "sk-a", Weight: 1}}), ad, nil
	}); err != nil {
		t.Fatalf("热替换被拒: %v", err)
	}
	if resp, body := postChat(t, ts.URL, chatBody); resp.StatusCode != 200 {
		t.Fatalf("状态码 %d: %s", resp.StatusCode, body)
	}
	if len(rp2.hosts()) == 0 {
		t.Fatal("仅 proxy_url 变更也必须经新代理出站(复用分支不得吞变更)")
	}
}

// TestOutboundProxy_未变渠道client同实例复用 评审 A9(a):热替换中配置未变的渠道
// client 指针保持不变(连接池无损的实质断言,超越 stats 计数)。
func TestOutboundProxy_未变渠道client同实例复用(t *testing.T) {
	upA := testkit.StartJSONUpstream(t, 200, chatCompletionJSON)
	rp := newRecordingProxy(t)

	reg := adapter.NewRegistry()
	reg.Register("pa", adapter.NewOpenAI(upA.URL(), "sk-a"))
	p := New(Config{
		Registry:  reg,
		Providers: []config.Provider{{Name: "pa", Adapter: "openai", BaseURL: upA.URL(), Weight: 1, ProxyURL: rp.URL}},
		Routes:    map[string][]config.Candidate{"alias-a": {{Provider: "pa", Model: "m", Priority: 0}}},
		Timeouts:  defTimeouts(),
		Pools: map[string]*keypool.Pool{
			"pa": keypool.NewPool([]keypool.KeyInput{{ID: "KA", Value: "sk-a", Weight: 1}}),
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	before := p.clientFor(p.topo.Load(), "pa")
	if _, err := p.ReplaceTopology(Config{
		Providers: []config.Provider{{Name: "pa", Adapter: "openai", BaseURL: upA.URL(), Weight: 1, ProxyURL: rp.URL}},
		Routes:    map[string][]config.Candidate{"alias-a": {{Provider: "pa", Model: "m", Priority: 0}}},
	}, func(prov config.Provider) (*keypool.Pool, adapter.Adapter, error) {
		ad, _ := reg.Get(prov.Name)
		return keypool.NewPool([]keypool.KeyInput{{ID: "KA", Value: "sk-a", Weight: 1}}), ad, nil
	}); err != nil {
		t.Fatalf("热替换被拒: %v", err)
	}
	if after := p.clientFor(p.topo.Load(), "pa"); after != before {
		t.Fatal("配置未变渠道的 client 必须同实例复用(连接池无损)")
	}
}

// TestOutboundProxy_env免疫的结构性断言 评审 A9 收口:不走 env 注入(ProxyFromEnvironment
// 进程级单次缓存,注入式测试顺序脆弱),直接断言免疫的结构依据——声明渠道 client 的
// Transport.Proxy 恒解析为声明 URL(不查环境);零声明渠道恒为 ProxyFromEnvironment 函数身份。
// 未来有人给代理 client 加「env 回退」或给默认 client 换固定代理,两断言分别变红。
func TestOutboundProxy_env免疫的结构性断言(t *testing.T) {
	upA := testkit.StartJSONUpstream(t, 200, chatCompletionJSON)
	upB := testkit.StartJSONUpstream(t, 200, chatCompletionJSON)
	rp := newRecordingProxy(t)
	p, _ := newDualProviderProxy(t, upA.URL(), upB.URL(), rp.URL) // pa 声明代理,pb 直连
	snap := p.topo.Load()

	declared := p.clientFor(snap, "pa").Transport.(*http.Transport)
	got, err := declared.Proxy(httptest.NewRequest("GET", "http://placeholder", nil))
	if err != nil || got == nil || got.String() != rp.URL {
		t.Fatalf("声明渠道 Proxy 应恒为声明 URL(不得查环境): %v %v", got, err)
	}
	direct := p.clientFor(snap, "pb").Transport.(*http.Transport)
	if direct.Proxy == nil {
		t.Fatal("零声明渠道必须保留 ProxyFromEnvironment(env 现状语义)")
	}
	// 函数值身份比较(vet 禁 fmt.Sprint 于 func):指针相等即 ProxyFromEnvironment 本体
	if reflect.ValueOf(direct.Proxy).Pointer() != reflect.ValueOf(http.ProxyFromEnvironment).Pointer() {
		t.Fatal("零声明渠道的 Proxy 应为 http.ProxyFromEnvironment 本体(env 现状语义)")
	}
}
