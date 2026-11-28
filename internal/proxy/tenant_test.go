package proxy

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/solvela/muniu/internal/auth"
	"github.com/solvela/muniu/internal/config"
)

func TestAcquireTenant_上限与释放(t *testing.T) {
	p := &Proxy{tenantInflight: map[string]int{}, tenantDefaultMaxFlight: 1}
	r := httptest.NewRequestWithContext(context.Background(), "POST", "/v1/chat/completions", nil)
	r = r.WithContext(auth.WithKeyInfo(r.Context(), &auth.KeyInfo{TenantID: "t1"}))
	release, ok := p.acquireTenant(r)
	if !ok {
		t.Fatal("首请求应放行")
	}
	if _, ok := p.acquireTenant(r); ok {
		t.Fatal("第二请求应被上限拒绝")
	}
	release()
	if _, ok := p.acquireTenant(r); !ok {
		t.Fatal("释放后应恢复放行")
	}
	// 其他租户不受 A 的占用影响
	r2 := httptest.NewRequestWithContext(context.Background(), "POST", "/v1/chat/completions", nil)
	r2 = r2.WithContext(auth.WithKeyInfo(r2.Context(), &auth.KeyInfo{TenantID: "t2"}))
	if _, ok := p.acquireTenant(r2); !ok {
		t.Fatal("其他租户不应被 A 的在途占用影响")
	}
	// 未认证请求恒放行
	if _, ok := p.acquireTenant(httptest.NewRequestWithContext(context.Background(), "POST", "/", nil)); !ok {
		t.Fatal("未认证请求不应被租户闸拦截")
	}
	// 0=不限
	p0 := &Proxy{tenantInflight: map[string]int{}}
	if _, ok := p0.acquireTenant(r); !ok {
		t.Fatal("上限 0 应不限")
	}
	if _, ok := p0.acquireTenant(r); !ok {
		t.Fatal("上限 0 应允许任意在途")
	}
}

func laneTenantRequest(tenantID string) *http.Request {
	r := httptest.NewRequestWithContext(context.Background(), "POST", "/v1/chat/completions", nil)
	return r.WithContext(auth.WithKeyInfo(r.Context(), &auth.KeyInfo{TenantID: tenantID}))
}

// managed 快照上限覆盖本地覆盖表与缺省;缺席回落本地。
func TestAcquireTenant_managed覆盖与水位(t *testing.T) {
	p := &Proxy{
		tenantInflight:         map[string]int{},
		tenantDefaultMaxFlight: 1,
		tenantMaxFlight:        map[string]int{"t1": 1},
		tenantManagedMaxFlight: map[string]int{},
		logger:                 slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	r := laneTenantRequest("t1")
	p.ReplaceManagedTenantLimits(map[string]int{"t1": 2})
	release1, ok := p.acquireTenant(r)
	if !ok {
		t.Fatal("managed 上限 2 应放行首个")
	}
	release2, ok := p.acquireTenant(r)
	if !ok {
		t.Fatal("managed 上限 2 应放行第二个")
	}
	if _, ok := p.acquireTenant(r); ok {
		t.Fatal("超过 managed 上限应拒绝")
	}
	if got := p.TenantInflightSamples(); len(got) != 1 || got[0].Value != 2 {
		t.Fatalf("在途水位应为 2: %+v", got)
	}
	release1()
	release2()
	if got := p.TenantInflightSamples(); len(got) != 1 || got[0].Value != 0 {
		t.Fatalf("释放后水位应回 0: %+v", got)
	}
	// 显式 0 = managed 不限,覆盖本地上限 1。
	p.ReplaceManagedTenantLimits(map[string]int{"t1": 0})
	release3, ok := p.acquireTenant(r)
	if !ok {
		t.Fatal("managed 0 应不限")
	}
	release4, ok := p.acquireTenant(r)
	if !ok {
		t.Fatal("managed 0 应允许任意在途")
	}
	release3()
	release4()
	// 快照缺席回落本地覆盖表 1。
	p.ReplaceManagedTenantLimits(nil)
	release, ok := p.acquireTenant(r)
	if !ok {
		t.Fatal("回落后首个应放行")
	}
	if _, ok := p.acquireTenant(r); ok {
		t.Fatal("回落后应按本地上限拒绝")
	}
	release()
}

// 所有 lane 的准入都在解析/上游之前:被拒绝的 embeddings/images 请求不触上游。
func TestEmbeddingsImages_租户准入(t *testing.T) {
	p := &Proxy{
		tenantInflight:         map[string]int{},
		tenantDefaultMaxFlight: 1,
		logger:                 slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	held, ok := p.acquireTenant(laneTenantRequest("t1"))
	if !ok {
		t.Fatal("占位请求应准入")
	}
	defer held()
	for name, h := range map[string]http.Handler{
		"embeddings": p.HandlerEmbeddings(),
		"images":     p.HandlerImages(),
	} {
		t.Run(name, func(t *testing.T) {
			r := laneTenantRequest("t1")
			r.Body = io.NopCloser(strings.NewReader(`{}`))
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != http.StatusTooManyRequests || !strings.Contains(w.Body.String(), "tenant_inflight_exceeded") {
				t.Fatalf("超限 lane 应 429: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

// chat 的租户闸仍在候选循环前生效;缓存命中/预算拒绝的零上游出口不占名额由装配顺序保证。
func TestChat_租户准入(t *testing.T) {
	p := New(Config{
		Routes: map[string][]config.Candidate{
			"alias": {{Provider: "p", Model: "up", Weight: 1}},
		},
		TenantDefaultMaxInflight: 1,
		Logger:                   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	held, ok := p.acquireTenant(laneTenantRequest("t1"))
	if !ok {
		t.Fatal("占位请求应准入")
	}
	defer held()
	r := laneTenantRequest("t1")
	r.Body = io.NopCloser(strings.NewReader(`{"model":"alias","messages":[{"role":"user","content":"hi"}]}`))
	w := httptest.NewRecorder()
	p.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusTooManyRequests || !strings.Contains(w.Body.String(), "tenant_inflight_exceeded") {
		t.Fatalf("chat 超限应 429: %d %s", w.Code, w.Body.String())
	}
}
