package admin

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/solvela/muniu/internal/store"
)

// fillRoutePath 把路由模式中的路径参数替换为占位值,供矩阵请求直达处理器。
func fillRoutePath(pattern string) (method, path string) {
	parts := strings.Fields(pattern)
	if len(parts) != 2 {
		return pattern, ""
	}
	segs := strings.Split(parts[1], "/")
	for i, s := range segs {
		if strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}") {
			segs[i] = "probe"
		}
	}
	return parts[0], strings.Join(segs, "/")
}

// 路由 × actor 矩阵:空绑定全拒;多绑定仅放行 tenant-scoped;未分类按 global-only 兜底;
// 停用主体 401 且不新增审计行。
func TestScopeMatrix_路由与主体矩阵(t *testing.T) {
	h, d, svc := scopeTestEnv(t)
	ctx := context.Background()
	t1, err := store.CreateTenant(ctx, d.DB, "t1")
	if err != nil {
		t.Fatal(err)
	}
	t2, err := store.CreateTenant(ctx, d.DB, "t2")
	if err != nil {
		t.Fatal(err)
	}
	unboundToken, _, err := svc.LoginFederated(ctx, "hufu:unbound", "无绑定")
	if err != nil {
		t.Fatal(err)
	}
	boundToken, p, err := svc.LoginFederated(ctx, "hufu:bound", "多绑定")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetAdminPrincipalScope(ctx, d.DB, p.Subject, "TENANT", []string{t1, t2}); err != nil {
		t.Fatal(err)
	}

	for _, e := range adminRouteTable {
		class := e.effectiveClass()
		if class == routeExempt {
			continue
		}
		method, path := fillRoutePath(e.Pattern)
		rr := serve(h, jsonSessionRequest(t, method, path, "", unboundToken))
		if rr.Code != 403 {
			t.Errorf("空绑定访问 %s 应 403,实际 %d %s", e.Pattern, rr.Code, rr.Body.String())
		}
		rr = serve(h, jsonSessionRequest(t, method, path, "", boundToken))
		switch class {
		case routeGlobalOnly:
			if rr.Code != 403 {
				t.Errorf("多绑定访问 global-only %s 应 403,实际 %d", e.Pattern, rr.Code)
			}
		case routeTenantScoped:
			if rr.Code == 403 {
				t.Errorf("多绑定访问 tenant-scoped %s 不应 403,实际 %d %s", e.Pattern, rr.Code, rr.Body.String())
			}
		}
	}

	// 未分类条目按 global-only 执行,scoped 主体默认拒绝。
	unclassified := adminRouteEntry{Pattern: "GET /admin/v1/probe/unclassified"}
	if unclassified.effectiveClass() != routeGlobalOnly {
		t.Fatal("未分类条目必须按 global-only 兜底")
	}
	if AuthorizeScope(Actor{Kind: "federated", Scope: "TENANT", TenantIDs: []string{t1}},
		unclassified.effectiveClass(), "") {
		t.Fatal("未分类路由不得对 scoped 主体放行")
	}

	// 停用主体:存量会话下一请求 401,且不落审计。
	before, _, err := store.ListAdminAudit(ctx, d.DB, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Patch(ctx, p.Subject, true); err != nil {
		t.Fatal(err)
	}
	rr := serve(h, jsonSessionRequest(t, http.MethodGet, "/admin/v1/keys", "", boundToken))
	if rr.Code != 401 {
		t.Fatalf("停用主体应 401,实际 %d %s", rr.Code, rr.Body.String())
	}
	after, _, err := store.ListAdminAudit(ctx, d.DB, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("401 不落审计: before=%d after=%d", len(before), len(after))
	}
}
