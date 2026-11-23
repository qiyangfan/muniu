package admin

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/solvela/muniu/internal/auth"
	"github.com/solvela/muniu/internal/store"
)

// scopeTestEnv 装配真实管理面链(auth(audit(scope(mux))))与可登录的会话服务。
func scopeTestEnv(t *testing.T) (http.Handler, Deps, *auth.AdminService) {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "scope.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	svc, err := auth.NewAdminService(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	d := Deps{DB: db, AdminAuth: svc, Token: "test-token", Logger: logger}
	h := New(context.Background(), d)
	if h == nil {
		t.Fatal("管理面应挂载")
	}
	return h, d, svc
}

func sessionRequest(t *testing.T, method, path, token string) *http.Request {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), method, path, nil)
	req.AddCookie(&http.Cookie{Name: adminCookie, Value: token})
	return req
}

// 路由级准入与审计:空绑定 fail-closed、global-only 403、GET 拒绝也留 denied、401 零回归。
func TestScopeMiddleware_路由准入与审计留痕(t *testing.T) {
	h, d, svc := scopeTestEnv(t)
	ctx := context.Background()
	unboundToken, _, err := svc.LoginFederated(ctx, "keycloak:unbound", "无绑定", testAdminRoles)
	if err != nil {
		t.Fatal(err)
	}
	boundToken, p, err := svc.LoginFederated(ctx, "keycloak:bound", "有绑定", testAdminRoles)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetAdminPrincipalScope(ctx, d.DB, p.Subject, "TENANT",
		[]string{"01900000-0000-7000-8000-000000000001"}); err != nil {
		t.Fatal(err)
	}

	// 空绑定 TENANT:whoami 可达,其余一律 403。
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, sessionRequest(t, http.MethodGet, "/admin/v1/keys", unboundToken))
	if rr.Code != 403 {
		t.Fatalf("空绑定应 403,实际 %d %s", rr.Code, rr.Body.String())
	}
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, sessionRequest(t, http.MethodGet, "/admin/v1/auth/whoami", unboundToken))
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"scope":"TENANT"`) {
		t.Fatalf("whoami 对空绑定必须可达且回显范围: %d %s", rr.Code, rr.Body.String())
	}
	audits, _, err := store.ListAdminAudit(ctx, d.DB, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(audits) != 1 || audits[0].Outcome != "denied" || !strings.Contains(audits[0].Detail, "scope_unbound") {
		t.Fatalf("GET 拒绝必须留 denied 审计(含空绑定原因): %+v", audits)
	}

	// 有绑定 TENANT:global-only 403(原因码区分),tenant-scoped 列表放行。
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, sessionRequest(t, http.MethodGet, "/admin/v1/providers", boundToken))
	if rr.Code != 403 {
		t.Fatalf("global-only 应 403,实际 %d", rr.Code)
	}
	audits, _, _ = store.ListAdminAudit(ctx, d.DB, "", 50)
	if len(audits) != 2 || !strings.Contains(audits[0].Detail, "scope_global_only") {
		t.Fatalf("global-only 拒绝原因码缺失: %+v", audits)
	}
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, sessionRequest(t, http.MethodGet, "/admin/v1/keys", boundToken))
	if rr.Code != 200 {
		t.Fatalf("tenant-scoped 列表应放行(过滤归处理器),实际 %d %s", rr.Code, rr.Body.String())
	}

	// 401 零回归:无凭据在 auth 层短路,不产生审计行。
	before := len(audits)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/admin/v1/providers", nil))
	if rr.Code != 401 {
		t.Fatalf("无凭据应 401,实际 %d", rr.Code)
	}
	audits, _, _ = store.ListAdminAudit(ctx, d.DB, "", 50)
	if len(audits) != before {
		t.Fatalf("401 不落审计: before=%d after=%d", before, len(audits))
	}
}

// AuthorizeScope 单点准入:GLOBAL 全放行,TENANT 受绑定与路由类约束,未知/空绑定失败关闭。
func TestAuthorizeScope_允许与拒绝两态(t *testing.T) {
	global := Actor{Kind: "token", Scope: "GLOBAL", TenantIDs: []string{}}
	scoped := Actor{Kind: "federated", Scope: "TENANT", TenantIDs: []string{"t1"}}
	unbound := Actor{Kind: "federated", Scope: "TENANT", TenantIDs: []string{}}
	unknown := Actor{Kind: "federated", Scope: ""}
	cases := []struct {
		name    string
		actor   Actor
		class   routeClass
		tenant  string
		allowed bool
	}{
		{"GLOBAL 访问 global-only", global, routeGlobalOnly, "", true},
		{"GLOBAL 访问 tenant-scoped 资源", global, routeTenantScoped, "t9", true},
		{"豁免路由对空 actor 可达", Actor{}, routeExempt, "", true},
		{"TENANT 访问 global-only", scoped, routeGlobalOnly, "", false},
		{"TENANT 列表无资源租户", scoped, routeTenantScoped, "", true},
		{"TENANT 命中绑定租户", scoped, routeTenantScoped, "t1", true},
		{"TENANT 越界资源租户", scoped, routeTenantScoped, "t2", false},
		{"TENANT 空绑定 fail-closed", unbound, routeTenantScoped, "", false},
		{"未知范围 fail-closed", unknown, routeTenantScoped, "", false},
	}
	for _, c := range cases {
		if got := AuthorizeScope(c.actor, c.class, c.tenant); got != c.allowed {
			t.Errorf("%s: 期望 %v,实际 %v", c.name, c.allowed, got)
		}
	}
}
