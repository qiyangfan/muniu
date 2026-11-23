package admin_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"database/sql"

	"github.com/solvela/muniu/internal/admin"
	"github.com/solvela/muniu/internal/server"
	"github.com/solvela/muniu/internal/store"
)

// loginCookie 经联邦轨签出一枚真实会话 cookie,审计用例据此走已认证变更路径。
// 首登默认 TENANT 空绑定(失败关闭);审计/管理面生命周期用例模拟存量全局管理员。
func loginCookie(t *testing.T, db *sql.DB, h http.Handler, idp *testIdP) *http.Cookie {
	t.Helper()
	cookie, _ := federatedLogin(t, h, idp)
	if err := store.SetAdminPrincipalScope(context.Background(), db, "jinguan-sub-1", "GLOBAL", nil); err != nil {
		t.Fatal(err)
	}
	return cookie
}

func TestAdminOperators_双轨审计生命周期(t *testing.T) {
	db := openDB(t)
	idp := newTestIdP(t)
	h, svc := newFederatedAdmin(t, db, "admin-test", idp.rp())
	cookie := loginCookie(t, db, h, idp)
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatal("cookie 硬化缺失")
	}
	call := func(method, path, body, token string, cookie *http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequestWithContext(context.Background(), method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for _, tc := range []struct {
		token  string
		cookie *http.Cookie
		name   string
	}{{"", cookie, "张三"}, {"admin-test", nil, "management-token"}} {
		w := call("GET", "/admin/v1/auth/whoami", "", tc.token, tc.cookie)
		if w.Code != 200 || !strings.Contains(w.Body.String(), tc.name) {
			t.Fatalf("whoami %d %s", w.Code, w.Body.String())
		}
	}
	w := call("POST", "/admin/v1/tenants", `{"name":"audit-target","password":"private","nested":{"secret":"private"},"daily_quota_credits":9007199254740993}`, "admin-test", nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = call("GET", "/admin/v1/admins", "", "", cookie)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "jinguan-sub-1") || strings.Contains(w.Body.String(), "password") {
		t.Fatal("主体列表应以 sub 为主键且不泄漏敏感字段", w.Body.String())
	}
	// 再补一条审计行,使首"页"(limit=1)必然还有下一页游标
	if w := call("POST", "/admin/v1/tenants", `{"name":"audit-target-2"}`, "admin-test", nil); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var before, after int
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM admin_audit_log`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	w = call("GET", "/admin/v1/audit?limit=1", "", "", cookie)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var page struct {
		Items []map[string]any `json:"items"`
		Next  string           `json:"next_cursor"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Next == "" {
		t.Fatal("分页", w.Body.String())
	}
	w = call("GET", "/admin/v1/audit?limit=1&cursor="+page.Next, "", "", cookie)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM admin_audit_log`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("GET 被审计")
	}
	var exact int
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM admin_audit_log WHERE detail LIKE '%9007199254740993%'`).Scan(&exact); err != nil || exact != 1 {
		t.Fatal("审计丢失整数精度", exact, err)
	}

	var leak int
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM admin_audit_log WHERE detail LIKE '%private%'`).Scan(&leak); err != nil || leak != 0 {
		t.Fatal("审计泄漏", leak, err)
	}
	w = call("PATCH", "/admin/v1/admins/jinguan-sub-1", `{"disabled":true}`, "admin-test", nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = call("GET", "/admin/v1/auth/whoami", "", "", cookie)
	if w.Code != 401 || !strings.Contains(w.Body.String(), "管理 token 无效") {
		t.Fatal("旧会话未失效/信封变化", w.Body.String())
	}
	// 无 token 但仍有活跃主体时照常挂载;主体全停用时整体关闭。
	if h := admin.New(context.Background(), admin.Deps{DB: db, AdminAuth: svc}); h != nil {
		t.Fatal("无活跃主体且无令牌时管理面必须关闭")
	}
	if err := svc.Patch(context.Background(), "jinguan-sub-1", false); err != nil {
		t.Fatal(err)
	}
	if admin.New(context.Background(), admin.Deps{DB: db, AdminAuth: svc}) == nil {
		t.Fatal("纯主体管理面未挂载")
	}
}

func TestAdminOperators_严格JSON与拒绝路径审计(t *testing.T) {
	db := openDB(t)
	idp := newTestIdP(t)
	h, _ := newFederatedAdmin(t, db, "admin-test", idp.rp())
	cookie := loginCookie(t, db, h, idp)
	request := func(method, path, body string, origin string, c *http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequestWithContext(context.Background(), method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if c != nil {
			r.AddCookie(c)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	w := request("PATCH", "/admin/v1/admins/jinguan-sub-1", `{"disabled":false} {}`, "", cookie)
	if w.Code != 400 {
		t.Fatalf("多值 JSON 未拒: %d", w.Code)
	}
	w = request("PATCH", "/admin/v1/admins/jinguan-sub-1", `{"Disabled":false}`, "", cookie)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	target := "jinguan-sub-1"
	if err := db.QueryRowContext(context.Background(), `SELECT target_id FROM admin_audit_log WHERE outcome='ok' ORDER BY created_at DESC,id DESC LIMIT 1`).Scan(&target); err != nil || target != "jinguan-sub-1" {
		t.Fatal("大小写字段请求未落审计目标", target, err)
	}
	w = request("PATCH", "/admin/v1/admins/jinguan-sub-1", `{"disabled":false}`, "https://evil.example", cookie)
	if w.Code != 403 {
		t.Fatal("跨站变更未拒", w.Code)
	}
	var n int
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM admin_audit_log WHERE outcome='denied'`).Scan(&n); err != nil || n < 1 {
		t.Fatal("拒绝出口审计缺失", n, err)
	}
	w = request("PATCH", "/admin/v1/admins/jinguan-sub-1", strings.Repeat(" ", (1<<20)+1), "", cookie)
	if w.Code != 400 {
		t.Fatal("超大 body 未拒", w.Code)
	}
	var all int
	if err := db.QueryRowContext(context.Background(),
		`SELECT count(*) FROM admin_audit_log WHERE action <> 'auth.oidc.login'`).Scan(&all); err != nil || all != 4 {
		t.Fatal("body拒绝出口审计缺失", all, err)
	}
}

func TestAdminOperators_panic不得审计为成功(t *testing.T) {
	db := openDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := server.Chain(admin.New(context.Background(), admin.Deps{DB: db, Token: "panic-token", Logger: logger, UpdateImagePricing: func(context.Context, string, *store.ImagePricingRecord) error { panic("fixture") }}), server.Recover(logger))
	code, body := call(t, h, "PUT", "/admin/v1/image-pricing/paint", "panic-token", `{"currency":"CNY","default":"0.1","tiers":{}}`)
	if code != 500 {
		t.Fatalf("没有正确recover: %d %s", code, body)
	}
	var outcome string
	if err := db.QueryRowContext(context.Background(), `SELECT outcome FROM admin_audit_log`).Scan(&outcome); err != nil {
		t.Fatal(err)
	}
	if outcome != "error:500" {
		t.Fatalf("panic审计为 %s", outcome)
	}
}
