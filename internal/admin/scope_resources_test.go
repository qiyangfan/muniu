package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/solvela/muniu/internal/auth"
	"github.com/solvela/muniu/internal/store"
)

func jsonSessionRequest(t *testing.T, method, path, body string, cookieToken string) *http.Request {
	t.Helper()
	var rd *strings.Reader
	if body == "" {
		rd = strings.NewReader("")
	} else {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequestWithContext(context.Background(), method, path, rd)
	req.Header.Set("Content-Type", "application/json")
	if cookieToken != "" {
		req.AddCookie(&http.Cookie{Name: adminCookie, Value: cookieToken})
	}
	return req
}

func bearerRequest(method, path, body, token string) *http.Request {
	var req *http.Request
	if body == "" {
		req = httptest.NewRequestWithContext(context.Background(), method, path, nil)
	} else {
		req = httptest.NewRequestWithContext(context.Background(), method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return req
}

func serve(h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// keys 读写收敛:列表按绑定过滤,越权签发/调整/吊销一律 404、不写库、留 denied 审计。
func TestTenantScope_Keys读写收敛(t *testing.T) {
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
	_, k1, err := auth.CreateKey(ctx, d.DB, "t1", "k1", 60, auth.KeyOpts{})
	if err != nil {
		t.Fatal(err)
	}
	_, k2, err := auth.CreateKey(ctx, d.DB, "t2", "k2", 60, auth.KeyOpts{})
	if err != nil {
		t.Fatal(err)
	}
	token, p, err := svc.LoginFederated(ctx, "keycloak:op1", "租户操作员", testAdminRoles)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetAdminPrincipalScope(ctx, d.DB, p.Subject, "TENANT", []string{t1}); err != nil {
		t.Fatal(err)
	}

	rr := serve(h, jsonSessionRequest(t, http.MethodGet, "/admin/v1/keys", "", token))
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), k1.ID) || strings.Contains(rr.Body.String(), k2.ID) {
		t.Fatalf("列表必须只含绑定租户的 key: %d %s", rr.Code, rr.Body.String())
	}
	rr = serve(h, jsonSessionRequest(t, http.MethodGet, "/admin/v1/tenants", "", token))
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), t1) || strings.Contains(rr.Body.String(), t2) {
		t.Fatalf("租户列表必须只含绑定租户: %d %s", rr.Code, rr.Body.String())
	}

	rr = serve(h, jsonSessionRequest(t, http.MethodPost, "/admin/v1/keys",
		`{"tenant":"t2","name":"越权签发"}`, token))
	if rr.Code != 404 || !strings.Contains(rr.Body.String(), "tenant_not_found") {
		t.Fatalf("越权签发应 404 tenant_not_found: %d %s", rr.Code, rr.Body.String())
	}
	all, err := auth.ListKeys(ctx, d.DB)
	if err != nil || len(all) != 2 {
		t.Fatalf("越权签发不得落库: %d %v", len(all), err)
	}
	rr = serve(h, jsonSessionRequest(t, http.MethodPost, "/admin/v1/keys",
		`{"tenant":"t1","name":"合法签发"}`, token))
	if rr.Code != 200 {
		t.Fatalf("绑定内签发应 200: %d %s", rr.Code, rr.Body.String())
	}

	rr = serve(h, jsonSessionRequest(t, http.MethodPatch, "/admin/v1/keys/"+k2.ID,
		`{"rpm_limit":999}`, token))
	if rr.Code != 404 {
		t.Fatalf("越权调整应 404: %d %s", rr.Code, rr.Body.String())
	}
	rr = serve(h, jsonSessionRequest(t, http.MethodDelete, "/admin/v1/keys/"+k2.Prefix, "", token))
	if rr.Code != 404 {
		t.Fatalf("越权吊销应 404: %d %s", rr.Code, rr.Body.String())
	}
	all, err = auth.ListKeys(ctx, d.DB)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range all {
		if k.ID == k2.ID && (k.RPMLimit != 60 || k.Status != "active") {
			t.Fatalf("越权操作不得产生副作用: %+v", k)
		}
	}

	audits, _, err := store.ListAdminAudit(ctx, d.DB, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	denied := 0
	for _, a := range audits {
		if a.Outcome == "denied" && strings.Contains(a.Detail, "tenant_out_of_scope") {
			denied++
		}
	}
	if denied < 3 {
		t.Fatalf("越权拒绝应逐次留 denied 审计: %d %+v", denied, audits)
	}
}

// usage 聚合:缺省绑定并集,显式参数必须子集,越界 404 且留 denied。
func TestTenantScope_Usage聚合收敛(t *testing.T) {
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
	now := time.Now().UTC()
	rows := []store.UsageRow{
		{ID: uuid.NewV7().String(), RequestID: "r1", TenantID: t1, APIKeyID: "k1",
			Provider: "deepseek", ModelAlias: "m", UpstreamModel: "u", Status: "ok",
			PromptTokens: 1, CostCredits: 10, Currency: "CNY", LatencyTotalMs: 1, CreatedAt: now},
		{ID: uuid.NewV7().String(), RequestID: "r2", TenantID: t2, APIKeyID: "k2",
			Provider: "deepseek", ModelAlias: "m", UpstreamModel: "u", Status: "ok",
			PromptTokens: 1, CostCredits: 20, Currency: "CNY", LatencyTotalMs: 1, CreatedAt: now},
		{ID: uuid.NewV7().String(), RequestID: "r3", TenantID: "t3-outside", APIKeyID: "k3",
			Provider: "deepseek", ModelAlias: "m", UpstreamModel: "u", Status: "ok",
			PromptTokens: 1, CostCredits: 40, Currency: "CNY", LatencyTotalMs: 1, CreatedAt: now},
	}
	if err := store.InsertUsageBatch(ctx, d.DB, rows); err != nil {
		t.Fatal(err)
	}
	token, p, err := svc.LoginFederated(ctx, "keycloak:op2", "用量操作员", testAdminRoles)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetAdminPrincipalScope(ctx, d.DB, p.Subject, "TENANT", []string{t1, t2}); err != nil {
		t.Fatal(err)
	}
	day := now.Format("2006-01-02")

	rr := serve(h, jsonSessionRequest(t, http.MethodGet, "/admin/v1/usage?group_by=day", "", token))
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"requests":2`) {
		t.Fatalf("缺省应绑定并集(t1+t2 共 2 次): %d %s", rr.Code, rr.Body.String())
	}
	rr = serve(h, jsonSessionRequest(t, http.MethodGet, "/admin/v1/usage?group_by=day&tenant_id="+t1, "", token))
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"requests":1`) || !strings.Contains(rr.Body.String(), day) {
		t.Fatalf("显式子集应只聚合 t1: %d %s", rr.Code, rr.Body.String())
	}
	rr = serve(h, jsonSessionRequest(t, http.MethodGet, "/admin/v1/usage?group_by=day&tenant_id=t3-outside", "", token))
	if rr.Code != 404 {
		t.Fatalf("越界租户参数应 404: %d %s", rr.Code, rr.Body.String())
	}
	audits, _, _ := store.ListAdminAudit(context.Background(), d.DB, "", 100)
	found := false
	for _, a := range audits {
		if a.Outcome == "denied" && strings.Contains(a.Detail, "tenant_out_of_scope") {
			found = true
		}
	}
	if !found {
		t.Fatalf("越界查询应留 denied 审计: %+v", audits)
	}
}

// 范围授予契约:整包替换、省略不动、GLOBAL 带绑定与未知取值 400,仅 GLOBAL 可调用。
func TestTenantScope_Admins授予契约(t *testing.T) {
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
	opToken, p, err := svc.LoginFederated(ctx, "keycloak:op3", "被授予者", testAdminRoles)
	if err != nil {
		t.Fatal(err)
	}
	opPath := "/admin/v1/admins/" + p.Subject

	rr := serve(h, bearerRequest(http.MethodGet, "/admin/v1/admins", "", "test-token"))
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"scope":"TENANT"`) ||
		!strings.Contains(rr.Body.String(), `"tenant_ids":[]`) {
		t.Fatalf("主体列表应纯增 scope/tenant_ids: %d %s", rr.Code, rr.Body.String())
	}
	rr = serve(h, bearerRequest(http.MethodPatch, opPath, `{"scope":"TENANT","tenant_ids":["`+t1+`","`+t2+`"]}`, "test-token"))
	if rr.Code != 200 {
		t.Fatalf("授予范围应 200: %d %s", rr.Code, rr.Body.String())
	}
	ids, _ := store.AdminPrincipalTenants(ctx, d.DB, p.Subject)
	if len(ids) != 2 {
		t.Fatalf("授予后绑定应为 2: %v", ids)
	}
	audits, _, err := store.ListAdminAudit(ctx, d.DB, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	granted := false
	for _, a := range audits {
		if a.Action == "admins.patch" && a.Outcome == "ok" && a.TargetID == p.Subject &&
			strings.Contains(a.Detail, `"scope":"TENANT"`) && strings.Contains(a.Detail, t1) {
			granted = true
		}
	}
	if !granted {
		t.Fatalf("授予审计必须记录 scope 与 tenant_ids: %+v", audits)
	}

	if rr = serve(h, bearerRequest(http.MethodPatch, opPath, `{"scope":"GLOBAL"}`, "test-token")); rr.Code != 400 {
		t.Fatalf("GLOBAL 携带既有绑定必须 400: %d %s", rr.Code, rr.Body.String())
	}
	if rr = serve(h, bearerRequest(http.MethodPatch, opPath, `{"scope":"GLOBAL","tenant_ids":[]}`, "test-token")); rr.Code != 200 {
		t.Fatalf("显式清空绑定提升 GLOBAL: %d %s", rr.Code, rr.Body.String())
	}
	if rr = serve(h, bearerRequest(http.MethodPatch, opPath, `{"tenant_ids":["no-such"]}`, "test-token")); rr.Code != 400 {
		t.Fatalf("未知租户必须 400: %d %s", rr.Code, rr.Body.String())
	}
	if rr = serve(h, bearerRequest(http.MethodPatch, opPath, `{"scope":"BOGUS"}`, "test-token")); rr.Code != 400 {
		t.Fatalf("未知 scope 必须 400: %d %s", rr.Code, rr.Body.String())
	}
	if rr = serve(h, bearerRequest(http.MethodPatch, opPath, `{}`, "test-token")); rr.Code != 400 {
		t.Fatalf("空 patch 必须 400: %d %s", rr.Code, rr.Body.String())
	}
	// 只传 disabled:scope 不动(旧客户端兼容)。
	if rr = serve(h, bearerRequest(http.MethodPatch, opPath, `{"disabled":false}`, "test-token")); rr.Code != 200 {
		t.Fatalf("仅 disabled 的旧客户端请求应保持兼容: %d %s", rr.Code, rr.Body.String())
	}
	got, _ := store.GetAdminPrincipal(ctx, d.DB, p.Subject)
	if got.Scope != "GLOBAL" {
		t.Fatalf("仅 disabled 不得改动 scope: %+v", got)
	}

	// TENANT actor 调用授予接口:路由 global-only 直接 403。
	if err := store.SetAdminPrincipalScope(ctx, d.DB, p.Subject, "TENANT", []string{t1}); err != nil {
		t.Fatal(err)
	}
	if rr = serve(h, jsonSessionRequest(t, http.MethodPatch, opPath, `{"tenant_ids":["`+t2+`"]}`, opToken)); rr.Code != 403 {
		t.Fatalf("TENANT actor 授予必须 403: %d %s", rr.Code, rr.Body.String())
	}
	ids, _ = store.AdminPrincipalTenants(ctx, d.DB, p.Subject)
	if len(ids) != 1 || ids[0] != t1 {
		t.Fatalf("被拒授予不得落库: %v", ids)
	}
}
