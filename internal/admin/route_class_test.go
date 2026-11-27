package admin

import "testing"

// 路由分类覆全断言:新增路由必须在此登记分类,漏标(零值)或误分类即红。
func TestAdminRouteTable_分类覆全且显式(t *testing.T) {
	want := map[string]routeClass{
		"POST /admin/v1/auth/logout":                 routeExempt,
		"POST /admin/v1/auth/logout/sso":             routeExempt,
		"GET /admin/v1/auth/whoami":                  routeExempt,
		"GET /admin/v1/auth/federation":              routeExempt,
		"GET /admin/v1/auth/oidc/start":              routeExempt,
		"GET /admin/v1/auth/oidc/callback":           routeExempt,
		"GET /admin/v1/admins":                       routeGlobalOnly,
		"PATCH /admin/v1/admins/{subject}":           routeGlobalOnly,
		"GET /admin/v1/audit":                        routeGlobalOnly,
		"GET /admin/v1/image-pricing":                routeGlobalOnly,
		"PUT /admin/v1/image-pricing/{alias}":        routeGlobalOnly,
		"DELETE /admin/v1/image-pricing/{alias}":     routeGlobalOnly,
		"GET /admin/v1/tenants":                      routeTenantScoped,
		"POST /admin/v1/tenants":                     routeGlobalOnly,
		"PATCH /admin/v1/tenants/{id}":               routeGlobalOnly,
		"GET /admin/v1/keys":                         routeTenantScoped,
		"POST /admin/v1/keys":                        routeTenantScoped,
		"PATCH /admin/v1/keys/{id}":                  routeTenantScoped,
		"DELETE /admin/v1/keys/{prefix}":             routeTenantScoped,
		"GET /admin/v1/usage":                        routeTenantScoped,
		"GET /admin/v1/providers":                    routeGlobalOnly,
		"GET /admin/v1/provider-presets":             routeGlobalOnly,
		"GET /admin/v1/cache/stats":                  routeGlobalOnly,
		"GET /admin/v1/pricing":                      routeGlobalOnly,
		"PUT /admin/v1/pricing/{alias}":              routeGlobalOnly,
		"DELETE /admin/v1/pricing/{alias}":           routeGlobalOnly,
		"GET /admin/v1/plugins":                      routeGlobalOnly,
		"PUT /admin/v1/plugins/{name}":               routeGlobalOnly,
		"PATCH /admin/v1/plugins/{name}":             routeGlobalOnly,
		"GET /admin/v1/balances":                     routeGlobalOnly,
		"POST /admin/v1/balances/{provider}/manual":  routeGlobalOnly,
		"POST /admin/v1/balances/{provider}/refresh": routeGlobalOnly,
		"GET /admin/v1/oauth/accounts":               routeGlobalOnly,
		"POST /admin/v1/oauth/accounts":              routeGlobalOnly,
		"DELETE /admin/v1/oauth/accounts/{id}":       routeGlobalOnly,
		"POST /admin/v1/oauth/authorize":             routeGlobalOnly,
		"GET /admin/v1/oauth/callback":               routeExempt,
		"GET /admin/v1/alerts/delivery-status":       routeGlobalOnly,
		"GET /admin/v1/upstream-health":              routeGlobalOnly,
		"GET /admin/v1/runtime/status":               routeGlobalOnly,
		"GET /admin/v1/runtime/shared-control":       routeGlobalOnly,
		"GET /admin/v1/runtime/commands":             routeGlobalOnly,
		"POST /admin/v1/runtime/commands":            routeGlobalOnly,
		"GET /admin/v1/runtime/logs":                 routeGlobalOnly,
	}
	if len(want) != len(adminRouteTable) {
		t.Fatalf("分类期望表 %d 条,路由表 %d 条:新增/删除路由必须同步登记分类", len(want), len(adminRouteTable))
	}
	seen := make(map[string]bool, len(adminRouteTable))
	for _, e := range adminRouteTable {
		if e.Class == routeUnclassified {
			t.Errorf("路由 %s 漏分类(零值哨兵)", e.Pattern)
		}
		if cls, ok := want[e.Pattern]; !ok {
			t.Errorf("路由 %s 未在分类期望表登记", e.Pattern)
		} else if cls != e.Class {
			t.Errorf("路由 %s 分类应为 %d,实际 %d", e.Pattern, cls, e.Class)
		}
		seen[e.Pattern] = true
	}
	for pattern := range want {
		if !seen[pattern] {
			t.Errorf("期望表声明了不存在的路由 %s", pattern)
		}
	}
}
