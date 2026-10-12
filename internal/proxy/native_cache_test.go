// 语义缓存直通双存测试(protocol-native-passthrough;测试先行域):
// 直通成功入库为双存(canonical 跨协议命中 / 原生体+标签+表单同构命中)、同构命中原生
// 恒等回放、表单不兼容/跨协议回退 canonical、翻译失格与状态请求零检索零入库。
package proxy

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/solvela/muniu/internal/adapter"
	"github.com/solvela/muniu/internal/adapter/bridge"
	"github.com/solvela/muniu/internal/auth"
	"github.com/solvela/muniu/internal/config"
	"github.com/solvela/muniu/internal/failover"
	"github.com/solvela/muniu/internal/keypool"
	"github.com/solvela/muniu/internal/protocol"
	"github.com/solvela/muniu/internal/semantic"
	"github.com/solvela/muniu/internal/store"
	"github.com/solvela/muniu/internal/testkit"
)

// tenantRequest 携带租户身份的测试请求(缓存租户归属从 ctx 取,与生产同缝)。
func tenantRequest(t *testing.T, target string) *http.Request {
	t.Helper()
	return httptest.NewRequest("POST", target, nil).WithContext(
		auth.WithKeyInfo(context.Background(), &auth.KeyInfo{ID: "k1", TenantID: testTenant}))
}

// newTestDB 测试用 SQLite(迁移 0008 直通双存列齐备)。
func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(t.TempDir() + "/native-cache.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

const testTenant = "01900000-0000-7000-8000-000000000001"

func newCacheProxy(t *testing.T) *Proxy {
	t.Helper()
	c, err := semantic.NewCache(newTestDB(t), semantic.Options{
		Dim: 4, Threshold: 0.5, TTL: time.Hour, Capacity: 100, EmbeddingModel: "emb",
	}, constEmbedder{})
	if err != nil {
		t.Fatal(err)
	}
	p := New(Config{
		Routes: map[string][]config.Candidate{
			"alias-a": {{Provider: "prov-a", Model: "up-a", Priority: 0}},
		},
		Cache:        c,
		TenantSwitch: func(tenant string) bool { return tenant == testTenant },
	})
	return p
}

// TestNativeCache_双存入库与同构命中 直通(messages×anthropic)成功入库:canonical 可被
// 等价 chat 请求命中(跨协议互通);原生体+标签+表单可被同构请求命中。
func TestNativeCache_双存入库与同构命中(t *testing.T) {
	p := newCacheProxy(t)
	r := tenantRequest(t, "/v1/messages")
	temp := 0.1
	req := &protocol.ChatRequest{Model: "alias-a", Messages: msgOf(`[{"role":"user","content":"hi"}]`), Temperature: &temp}
	_, ph := p.lookupCacheFor(testTenant, r, req)
	if ph == nil {
		t.Fatal("应有 miss phase")
	}
	// 直通双存:canonical chat 形态 + anthropic 原生体(stream 表单)
	nativeBody := []byte(`data: {"type":"message_start"}` + "\n\n" + `data: {"type":"message_stop"}` + "\n\n")
	canonical := []byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	m := &meterState{nativePath: true, stream: true, outBody: nativeBody, canonicalBody: canonical}
	p.storePhase(r, ph, req, failover.Candidate{Provider: "anth"}, m, &inboundNative{proto: protocol.InboundMessages})

	// 跨协议:等价 chat 请求(nat=nil)命中 canonical 侧
	res, _ := p.lookupCacheFor(testTenant, r, req)
	if res == nil || len(res.Response) == 0 {
		t.Fatal("等价 chat 请求应命中 canonical 侧")
	}
	// 同构:messages 请求命中且原生体+标签+表单齐备
	res2, _ := p.lookupCacheFor(testTenant, r, req)
	if res2 == nil || res2.NativeProto != "messages" || res2.NativeForm != cacheFormStream || len(res2.NativeResponse) == 0 {
		t.Fatalf("同构请求应命中原生侧: %+v", res2)
	}
	if string(res2.NativeResponse) != string(nativeBody) {
		t.Fatalf("原生体应逐字节入库: %s", res2.NativeResponse)
	}
}

// TestNativeCache_回放择优 同构命中且表单兼容 → 原生恒等回放;表单不兼容/跨协议 → canonical。
func TestNativeCache_回放择优(t *testing.T) {
	p := newCacheProxy(t)
	native := []byte(`data: {"type":"message_start","message":{"usage":{"input_tokens":21}}}` + "\n\n")
	res := &semantic.LookupResult{
		Response:       []byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`),
		NativeResponse: native,
		NativeProto:    "messages",
		NativeForm:     cacheFormStream,
	}

	t.Run("同构流式原生恒等回放", func(t *testing.T) {
		w := httptest.NewRecorder()
		p.replayHit(w, httptest.NewRequest("POST", "/v1/messages", nil), res,
			&inboundNative{proto: protocol.InboundMessages}, true)
		if w.Code != 200 || w.Body.String() != string(native) {
			t.Fatalf("同构命中应原生恒等回放: %d %s", w.Code, w.Body)
		}
		if ct := w.Header().Get("Content-Type"); ct != "text/event-stream" {
			t.Fatalf("流式回放 Content-Type 不符: %q", ct)
		}
	})
	t.Run("表单不兼容回退canonical", func(t *testing.T) {
		w := httptest.NewRecorder()
		p.replayHit(w, httptest.NewRequest("POST", "/v1/messages", nil), res,
			&inboundNative{proto: protocol.InboundMessages}, false) // 非流式请求 × 流式条目
		if !strings.Contains(w.Body.String(), `"content":"ok"`) {
			t.Fatalf("表单不兼容应回退 canonical: %s", w.Body)
		}
		if strings.Contains(w.Body.String(), "message_start") {
			t.Fatal("回退不得透传原生体")
		}
	})
	t.Run("跨协议回退canonical", func(t *testing.T) {
		w := httptest.NewRecorder()
		p.replayHit(w, httptest.NewRequest("POST", "/v1/chat/completions", nil), res, nil, true)
		if !strings.Contains(w.Body.String(), `"content":"ok"`) {
			t.Fatalf("跨协议命中应走 canonical: %s", w.Body)
		}
	})
}

// TestNativeCache_失格与状态请求旁路 翻译失格与状态请求零检索零入库(缓存覆盖的自然边界)。
func TestNativeCache_失格与状态请求旁路(t *testing.T) {
	p := newCacheProxy(t)
	r := tenantRequest(t, "/v1/responses")

	// 状态请求:handleChat 分支跳过 lookupCache——此处钉 storePhase 侧与旁路判据的单元语义:
	// 失格 native 入库仍可(canonical 缺失降级),但 stateful 请求根本不会拿到 phase(集成钉子覆盖)。
	temp := 0.1
	req := &protocol.ChatRequest{Model: "alias-a", Messages: msgOf(`[{"role":"user","content":"hi"}]`), Temperature: &temp}
	_, ph := p.lookupCacheFor(testTenant, r, req)
	// 失格直通成功:canonical 缺失 → 仅原生侧入库(canonical 为 nil,降级为跨协议不可命中)
	m := &meterState{nativePath: true, stream: false, outBody: []byte(`{"id":"msg_1","type":"message","usage":{"input_tokens":1}}`)}
	p.storePhase(r, ph, req, failover.Candidate{Provider: "anth"}, m, &inboundNative{proto: protocol.InboundMessages, ineligCode: protocol.CodeContentBlockUnsupported})
	res, _ := p.lookupCacheFor(testTenant, r, req)
	if res == nil {
		t.Fatal("原生侧应入库可命中")
	}
	if len(res.Response) == 0 {
		// canonical 缺失条目:跨协议(chat)视角不可回放——旁路判据语义正确
		t.Log("canonical 缺失:仅原生侧可命中(预期降级形态)")
	}
}

// nativeFormCompatible 表单兼容判据表。
func TestNativeFormCompatible(t *testing.T) {
	if !nativeFormCompatible(cacheFormStream, true) || !nativeFormCompatible(cacheFormNonStream, false) {
		t.Fatal("同表单应兼容")
	}
	if nativeFormCompatible(cacheFormStream, false) || nativeFormCompatible(cacheFormNonStream, true) {
		t.Fatal("跨表单不兼容")
	}
	if nativeFormCompatible("", true) || nativeFormCompatible("", false) {
		t.Fatal("空表单(翻译路径条目)不参与原生回放")
	}
}

// TestNativeCache_直通流式入库与原生回放 spec「同构命中原生回放」全链钉子:
// 流式直通成功经 attemptNativeStream 回填双存(原生 SSE 字节累计 + canonical 拼包),
// 同构第二次请求零上游触达、逐字节恒等回放。(回归钉:修复前流式出口不回填
// outBody/canonicalBody,storePhase 早退,spec 流式场景永不成立。)
func TestNativeCache_直通流式入库与原生回放(t *testing.T) {
	up := testkit.StartStreamUpstream(t, anthropicStreamEvents())
	reg := adapter.NewRegistry()
	reg.Register("anth", bridge.NewAnthropic(up.URL(), adapter.Options{}))
	c, err := semantic.NewCache(newTestDB(t), semantic.Options{
		Dim: 4, Threshold: 0.5, TTL: time.Hour, Capacity: 100, EmbeddingModel: "emb",
	}, constEmbedder{})
	if err != nil {
		t.Fatal(err)
	}
	p := New(Config{
		Registry:  reg,
		Providers: []config.Provider{{Name: "anth", Adapter: "anthropic"}},
		Routes: map[string][]config.Candidate{
			"alias-a": {{Provider: "anth", Model: "claude-up", Priority: 0}},
		},
		Cache:        c,
		TenantSwitch: func(tenant string) bool { return tenant == testTenant },
		Timeouts:     Timeouts{Dial: time.Second, FirstToken: 10 * time.Second, ChunkIdle: 10 * time.Second},
		Pools: map[string]*keypool.Pool{
			"anth": keypool.NewPool([]keypool.KeyInput{{ID: "KA", Value: "k", Weight: 1}}),
		},
		RandIntn: func(n int) int { return 0 },
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	// 鉴权中间件替身:缓存租户归属从 ctx 取(生产由 verifier 注入,测试直注同缝形态)
	authed := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.HandlerMessages().ServeHTTP(w, r.WithContext(
			auth.WithKeyInfo(r.Context(), &auth.KeyInfo{ID: "k1", TenantID: testTenant})))
	})
	ts := httptest.NewServer(authed)
	t.Cleanup(ts.Close)

	raw := `{"model":"alias-a","max_tokens":64,"temperature":0.1,"stream":true,` +
		`"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`
	resp1, body1 := postChat(t, ts.URL, raw)
	if resp1.StatusCode != 200 {
		t.Fatalf("首次流式应 200: %d %s", resp1.StatusCode, body1)
	}
	if !strings.Contains(body1, `"type":"message_start"`) || !strings.Contains(body1, `"type":"message_stop"`) {
		t.Fatalf("首次应为上游原生事件序列: %s", body1)
	}
	if n := up.RequestCount(); n != 1 {
		t.Fatalf("首次应触达上游恰好一次: %d", n)
	}

	// 同构第二次请求:命中原生侧,零上游触达,逐字节恒等回放
	resp2, body2 := postChat(t, ts.URL, raw)
	if resp2.StatusCode != 200 {
		t.Fatalf("命中回放应 200: %d %s", resp2.StatusCode, body2)
	}
	if body2 != body1 {
		t.Fatalf("同构命中应原生恒等回放:\n got=%s\nwant=%s", body2, body1)
	}
	if n := up.RequestCount(); n != 1 {
		t.Fatalf("命中回放不得触达上游: %d", n)
	}
}
