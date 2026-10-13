package admin_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/solvela/muniu/internal/admin"
	"github.com/solvela/muniu/internal/auth"
	"github.com/solvela/muniu/internal/server"
	"github.com/solvela/muniu/internal/store"
)

func TestAdminOperators_双轨审计生命周期(t *testing.T) {
	db := openDB(t)
	svc, err := auth.NewAdminService(db)
	if err != nil {
		t.Fatal(err)
	}
	u, err := svc.Create("alice", "password123")
	if err != nil {
		t.Fatal(err)
	}
	h := admin.New(admin.Deps{DB: db, Token: "admin-test", AdminAuth: svc})
	call := func(method, path, body, token string, cookie *http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
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
	w := call("POST", "/admin/v1/auth/login", `{"username":"alice","password":"password123"}`, "", nil)
	if w.Code != 200 || len(w.Result().Cookies()) != 1 {
		t.Fatalf("登录: %d %s", w.Code, w.Body.String())
	}
	cookie := w.Result().Cookies()[0]
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatal("cookie 硬化缺失")
	}
	for _, tc := range []struct {
		token  string
		cookie *http.Cookie
		name   string
	}{{"", cookie, "alice"}, {"admin-test", nil, "management-token"}} {
		w = call("GET", "/admin/v1/auth/whoami", "", tc.token, tc.cookie)
		if w.Code != 200 || !strings.Contains(w.Body.String(), tc.name) {
			t.Fatalf("whoami %d %s", w.Code, w.Body.String())
		}
	}
	w = call("POST", "/admin/v1/admins", `{"username":"bob","password":"password-bob","unknown":"do-not-log"}`, "", cookie)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = call("POST", "/admin/v1/tenants", `{"name":"audit-target","password":"private","nested":{"secret":"private"},"daily_quota_credits":9007199254740993}`, "admin-test", nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = call("GET", "/admin/v1/admins", "", "", cookie)
	if w.Code != 200 || strings.Contains(w.Body.String(), "password") {
		t.Fatal("账号列表泄漏", w.Body.String())
	}
	var before, after int
	if err := db.QueryRow(`SELECT count(*) FROM admin_audit_log`).Scan(&before); err != nil {
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
	if err := db.QueryRow(`SELECT count(*) FROM admin_audit_log`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("GET 被审计")
	}
	var exact int
	if err := db.QueryRow(`SELECT count(*) FROM admin_audit_log WHERE detail LIKE '%9007199254740993%'`).Scan(&exact); err != nil || exact != 1 {
		t.Fatal("审计丢失整数精度", exact, err)
	}

	var leak int
	if err := db.QueryRow(`SELECT count(*) FROM admin_audit_log WHERE detail LIKE '%password123%' OR detail LIKE '%password-bob%' OR detail LIKE '%private%' OR detail LIKE '%do-not-log%'`).Scan(&leak); err != nil || leak != 0 {
		t.Fatal("审计泄漏", leak, err)
	}
	w = call("PATCH", "/admin/v1/admins/"+u.ID, `{"disabled":true}`, "admin-test", nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = call("GET", "/admin/v1/auth/whoami", "", "", cookie)
	if w.Code != 401 || !strings.Contains(w.Body.String(), "管理 token 无效") {
		t.Fatal("旧会话未失效/信封变化", w.Body.String())
	}
	// 无 token 但有活跃账号时仍挂载；零凭据零账号时依旧整体关闭。
	if admin.New(admin.Deps{DB: db, AdminAuth: svc}) == nil {
		t.Fatal("纯账号管理面未挂载")
	}
	for i := 0; i < 6; i++ {
		w = call("POST", "/admin/v1/auth/login", `{"username":"nobody","password":"wrong"}`, "", nil)
		if w.Code != 401 {
			t.Fatal(w.Code)
		}
	}
	var denied int
	if err := db.QueryRow(`SELECT count(*) FROM admin_audit_log WHERE actor_kind='login' AND outcome='denied'`).Scan(&denied); err != nil || denied < 6 {
		t.Fatal("登录失败审计缺失", denied, err)
	}
}

func TestAdminOperators_严格JSON与拒绝路径审计(t *testing.T) {
	db := openDB(t)
	svc, err := auth.NewAdminService(db)
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.Create("alice", "password123")
	if err != nil {
		t.Fatal(err)
	}
	h := admin.New(admin.Deps{DB: db, Token: "admin-test", AdminAuth: svc})
	request := func(body string, origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/admin/v1/auth/login", strings.NewReader(body))
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	w := request(`{"username":"alice","password":"password123"} {}`, "")
	if w.Code != 400 {
		t.Fatalf("多值 JSON 未拒: %d", w.Code)
	}
	w = request(`{"Username":"alice","Password":"password123"}`, "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var name string
	if err := db.QueryRow(`SELECT actor_name FROM admin_audit_log WHERE outcome='ok' ORDER BY created_at DESC,id DESC LIMIT 1`).Scan(&name); err != nil || name != "alice" {
		t.Fatal("大小写字段归因丢失", name, err)
	}
	w = request(`{"username":"alice","password":"password123"}`, "https://evil.example")
	if w.Code != 403 {
		t.Fatal("跨站登录未拒", w.Code)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM admin_audit_log WHERE outcome='denied'`).Scan(&n); err != nil || n < 2 {
		t.Fatal("拒绝出口审计缺失", n, err)
	}
	w = request(strings.Repeat(" ", (1<<20)+1), "")
	if w.Code != 400 {
		t.Fatal("超大 body 未拒", w.Code)
	}
	var all int
	if err := db.QueryRow(`SELECT count(*) FROM admin_audit_log`).Scan(&all); err != nil || all != 4 {
		t.Fatal("body拒绝出口审计缺失", all, err)
	}
}

func TestAdminOperators_panic不得审计为成功(t *testing.T) {
	db := openDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := server.Chain(admin.New(admin.Deps{DB: db, Token: "panic-token", Logger: logger, UpdateImagePricing: func(string, *store.ImagePricingRecord) error { panic("fixture") }}), server.Recover(logger))
	code, body := call(t, h, "PUT", "/admin/v1/image-pricing/paint", "panic-token", `{"currency":"CNY","default":"0.1","tiers":{}}`)
	if code != 500 {
		t.Fatalf("没有正确recover: %d %s", code, body)
	}
	var outcome string
	if err := db.QueryRow(`SELECT outcome FROM admin_audit_log`).Scan(&outcome); err != nil {
		t.Fatal(err)
	}
	if outcome != "error:500" {
		t.Fatalf("panic审计为 %s", outcome)
	}
}
