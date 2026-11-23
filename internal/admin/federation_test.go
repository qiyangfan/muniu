package admin_test

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/solvela/muniu/internal/admin"
	"github.com/solvela/muniu/internal/auth"
	"github.com/solvela/muniu/internal/oidc"
	"github.com/solvela/muniu/internal/ratelimit"
	"github.com/solvela/muniu/internal/store"
)

var testAdminRoles = []string{auth.FederatedAdminRole}

// testIdP 测试用锦官权威:发现文档 + JWKS + token 端点;nonce/sub/name 由用例回填。
type testIdP struct {
	srv   *httptest.Server
	key   *rsa.PrivateKey
	nonce string
	sub   string
	name  string
	roles []string
}

func newTestIdP(t *testing.T) *testIdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	idp := &testIdP{key: key, sub: "jinguan-sub-1", name: "张三", roles: []string{auth.FederatedAdminRole}}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": idp.srv.URL, "authorization_endpoint": idp.srv.URL + "/authorize",
			"token_endpoint": idp.srv.URL + "/token", "jwks_uri": idp.srv.URL + "/jwks",
			"end_session_endpoint": idp.srv.URL + "/end_session",
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]any{{
			"kty": "RSA", "kid": "k1", "use": "sig",
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.PostForm.Get("code_verifier") == "" {
			http.Error(w, `{"error":"invalid_request"}`, http.StatusBadRequest)
			return
		}
		claims := map[string]any{
			"iss": idp.srv.URL, "aud": "muniu-console", "sub": idp.sub, "name": idp.name,
			"exp": time.Now().Add(time.Hour).Unix(), "nonce": idp.nonce,
			"resource_access": map[string]any{"muniu-console": map[string]any{"roles": idp.roles}},
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "at", "token_type": "Bearer", "expires_in": 3600,
			"id_token": idp.sign(claims),
		})
	})
	idp.srv = httptest.NewServer(mux)
	t.Cleanup(idp.srv.Close)
	return idp
}

func (f *testIdP) sign(claims map[string]any) string {
	hb, _ := json.Marshal(map[string]any{"alg": "RS256", "typ": "JWT", "kid": "k1"})
	cb, _ := json.Marshal(claims)
	input := base64.RawURLEncoding.EncodeToString(hb) + "." + base64.RawURLEncoding.EncodeToString(cb)
	sum := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, sum[:])
	if err != nil {
		panic(err)
	}
	return input + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func (f *testIdP) rp() *oidc.RelyingParty {
	return oidc.New(oidc.Config{
		Issuer: f.srv.URL, ClientID: "muniu-console",
		RedirectURL: "http://127.0.0.1:8787/admin/v1/auth/oidc/callback",
	}, f.srv.Client())
}

// newFederatedAdmin 构造带联邦轨的管理面;token 为空时验证「仅联邦也挂载」。
func newFederatedAdmin(t *testing.T, db *sql.DB, token string, rp *oidc.RelyingParty) (http.Handler, *auth.AdminService) {
	t.Helper()
	svc, err := auth.NewAdminService(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	h := admin.New(context.Background(), admin.Deps{
		DB: db, Token: token, AdminAuth: svc, Federated: rp,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if h == nil {
		t.Fatal("联邦已配置时管理面必须挂载")
	}
	return h, svc
}

// federatedLogin 走完 start → callback,返回会话 cookie 与 state。
func federatedLogin(t *testing.T, h http.Handler, idp *testIdP) (*http.Cookie, string) {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/admin/v1/auth/oidc/start", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("start 应 302,实际 %d: %s", rec.Code, rec.Body.String())
	}
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	state, nonce := loc.Query().Get("state"), loc.Query().Get("nonce")
	if state == "" || nonce == "" {
		t.Fatal("授权 URL 缺 state/nonce")
	}
	idp.nonce = nonce
	cb := httptest.NewRequestWithContext(context.Background(), http.MethodGet,
		"/admin/v1/auth/oidc/callback?code=c1&state="+url.QueryEscape(state), nil)
	for _, c := range rec.Result().Cookies() {
		if c.Name == "muniu_oidc_state" {
			cb.AddCookie(c)
		}
	}
	if c, err := cb.Cookie("muniu_oidc_state"); err != nil || c == nil {
		t.Fatal("start 未设置浏览器绑定 state cookie")
	}
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, cb)
	if rec2.Code != http.StatusFound || !strings.HasPrefix(rec2.Header().Get("Location"), "/console/") {
		t.Fatalf("callback 应回控制台,实际 %d %s", rec2.Code, rec2.Header().Get("Location"))
	}
	for _, c := range rec2.Result().Cookies() {
		if c.Name == "muniu_admin_session" && c.Value != "" {
			return c, state
		}
	}
	t.Fatal("callback 未签发会话 cookie")
	return nil, ""
}

func send(h http.Handler, method, path string, cookie *http.Cookie, body string) *httptest.ResponseRecorder {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequestWithContext(context.Background(), method, path, rd)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestFederation_双轨并存与归因 覆盖Keycloak会话轨与共享令牌轨并存、whoami 归因与审计归因;
// 本地账密登录路由必须不存在(去账号化的外部可观测契约)。
func TestFederation_双轨并存与联邦归因(t *testing.T) {
	db := openDB(t)
	idp := newTestIdP(t)
	h, _ := newFederatedAdmin(t, db, "shared-token", idp.rp())

	// 联邦入口公开可用,登录页据此决定是否渲染入口
	rec := send(h, http.MethodGet, "/admin/v1/auth/federation", nil, "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"enabled":true`) {
		t.Fatalf("federation 探测应 enabled: %d %s", rec.Code, rec.Body.String())
	}

	cookie, _ := federatedLogin(t, h, idp)
	// 首登默认 TENANT 空绑定;本用例模拟存量全局管理员,验证双轨归因与审计。
	if err := store.SetAdminPrincipalScope(context.Background(), db, "jinguan-sub-1", "GLOBAL", nil); err != nil {
		t.Fatal(err)
	}
	rec = send(h, http.MethodGet, "/admin/v1/auth/whoami", cookie, "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"kind":"federated"`) ||
		!strings.Contains(rec.Body.String(), "张三") {
		t.Fatalf("Keycloak会话 whoami 应归因 federated/实名: %d %s", rec.Code, rec.Body.String())
	}

	// 令牌轨零回归
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/admin/v1/auth/whoami", nil)
	req.Header.Set("Authorization", "Bearer shared-token")
	recT := httptest.NewRecorder()
	h.ServeHTTP(recT, req)
	if recT.Code != 200 || !strings.Contains(recT.Body.String(), `"kind":"token"`) {
		t.Fatalf("令牌轨零回归: %d %s", recT.Code, recT.Body.String())
	}

	// 本地账密登录已删除:GET 未匹配 405,POST 不豁免鉴权直接 401
	if rec := send(h, http.MethodGet, "/admin/v1/auth/login", nil, ""); rec.Code == 200 {
		t.Fatalf("本地登录路由必须不存在: %d", rec.Code)
	}
	if rec := send(h, http.MethodPost, "/admin/v1/auth/login", nil,
		`{"username":"localadmin","password":"pw-123456"}`); rec.Code != 401 ||
		!strings.Contains(rec.Body.String(), "auth_failed") {
		t.Fatalf("本地登录请求必须因未认证被拒: %d %s", rec.Code, rec.Body.String())
	}

	// 会话变更动作审计归因
	recP := send(h, http.MethodPatch, "/admin/v1/admins/"+url.PathEscape("jinguan-sub-1"), cookie, `{"disabled":false}`)
	if recP.Code != 200 {
		t.Fatalf("主体变更应获准: %d %s", recP.Code, recP.Body.String())
	}
	items, _, err := store.ListAdminAudit(context.Background(), db, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range items {
		if a.ActorKind == "federated" && a.ActorName == "张三" && a.TargetID == "jinguan-sub-1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("审计应含 federated/张三 且目标为该 sub: %+v", items)
	}
}

// TestFederation_sub即主体键 覆盖同一 sub 恒映射同一主体、同名不同 sub 并存,
// 且不再产生 uuid 影子 id 或 external_subject 映射。
func TestFederation_同一sub恒映射同一主体(t *testing.T) {
	db := openDB(t)
	idp := newTestIdP(t)
	h, _ := newFederatedAdmin(t, db, "tok", idp.rp())
	federatedLogin(t, h, idp)
	federatedLogin(t, h, idp)

	principals, err := store.ListAdminPrincipals(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if len(principals) != 1 || principals[0].Subject != "jinguan-sub-1" || principals[0].Username != "张三" {
		t.Fatalf("同一 sub 只应有一个主体且以 sub 为主键: %+v", principals)
	}
	// 同名不同 sub 是不同主体(身份键是 sub,不是展示名)
	idp.sub = "jinguan-sub-2"
	federatedLogin(t, h, idp)
	principals, err = store.ListAdminPrincipals(context.Background(), db)
	if err != nil || len(principals) != 2 {
		t.Fatalf("同名不同 sub 必须并存为两个主体: %+v %v", principals, err)
	}
	// 旧影子表不再写入
	var shadows int
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM admin_users`).Scan(&shadows); err != nil || shadows != 0 {
		t.Fatalf("去账号化后不得再写 admin_users: %d %v", shadows, err)
	}
}

// TestFederation_停用拒绝登录且撤销会话 覆盖停用主体拒绝再次登录、存量 cookie 即刻 401。
func TestFederation_停用账号拒绝登录(t *testing.T) {
	db := openDB(t)
	idp := newTestIdP(t)
	h, svc := newFederatedAdmin(t, db, "tok", idp.rp())
	cookie, _ := federatedLogin(t, h, idp)
	if err := svc.Patch(context.Background(), "jinguan-sub-1", true); err != nil {
		t.Fatal(err)
	}
	if rec := send(h, http.MethodGet, "/admin/v1/auth/whoami", cookie, ""); rec.Code != 401 {
		t.Fatalf("停用后存量会话必须 401: %d %s", rec.Code, rec.Body.String())
	}
	if _, _, err := svc.LoginFederated(context.Background(), "jinguan-sub-1", "张三", testAdminRoles); err == nil {
		t.Fatal("停用的主体必须拒绝登录")
	}
	rec := send(h, http.MethodGet, "/admin/v1/auth/oidc/start", nil, "")
	if rec.Code != http.StatusFound {
		t.Fatalf("停用不影响发起授权: %d", rec.Code)
	}
}

// TestFederation_登出撤销会话 覆盖 logout 删除服务端会话,cookie 重放失效。
func TestFederation_登出撤销会话(t *testing.T) {
	db := openDB(t)
	idp := newTestIdP(t)
	h, _ := newFederatedAdmin(t, db, "tok", idp.rp())
	cookie, _ := federatedLogin(t, h, idp)
	if rec := send(h, http.MethodPost, "/admin/v1/auth/logout", cookie, ""); rec.Code != 200 {
		t.Fatalf("注销应 200: %d %s", rec.Code, rec.Body.String())
	}
	if rec := send(h, http.MethodGet, "/admin/v1/auth/whoami", cookie, ""); rec.Code != 401 {
		t.Fatalf("注销后重放必须 401: %d %s", rec.Code, rec.Body.String())
	}
	var n int
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM admin_sessions`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("服务端会话未删除: %d %v", n, err)
	}
}

// TestFederation_回调缺浏览器绑定cookie被拒 覆盖登录 CSRF:回调 URL 转交他人无法完成登录。
func TestFederation_回调缺浏览器绑定cookie被拒(t *testing.T) {
	db := openDB(t)
	idp := newTestIdP(t)
	h, _ := newFederatedAdmin(t, db, "tok", idp.rp())
	rec := send(h, http.MethodGet, "/admin/v1/auth/oidc/start", nil, "")
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	state := loc.Query().Get("state")
	idp.nonce = loc.Query().Get("nonce")
	cb := send(h, http.MethodGet, "/admin/v1/auth/oidc/callback?code=c1&state="+url.QueryEscape(state), nil, "")
	if cb.Code != http.StatusFound || !strings.Contains(cb.Header().Get("Location"), "oidc_error=") {
		t.Fatalf("缺 state cookie 必须拒绝: %d %s", cb.Code, cb.Header().Get("Location"))
	}
	var n int
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM admin_sessions`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("拒绝路径不得签发会话: %d %v", n, err)
	}
	items, _, err := store.ListAdminAudit(context.Background(), db, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	denied := false
	for _, a := range items {
		if a.Action == "auth.oidc.login" && a.Outcome == "denied" && strings.Contains(a.Detail, "state_mismatch") {
			denied = true
		}
	}
	if !denied {
		t.Fatalf("拒绝路径必须写 auth.oidc.login 审计: %+v", items)
	}
}

// TestFederation_SSO登出返回Keycloakend_session 覆盖本地会话撤销与统一登出交接。
func TestFederation_SSO登出返回Keycloakend_session(t *testing.T) {
	db := openDB(t)
	idp := newTestIdP(t)
	h, _ := newFederatedAdmin(t, db, "tok", idp.rp())
	cookie, _ := federatedLogin(t, h, idp)
	rec := send(h, http.MethodPost, "/admin/v1/auth/logout/sso", cookie, "")
	if rec.Code != 200 {
		t.Fatalf("SSO 登出应 200: %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, idp.srv.URL+"/end_session") || !strings.Contains(body, "client_id=muniu-console") {
		t.Fatalf("应返回Keycloak end_session 地址: %s", body)
	}
	if !strings.Contains(body, url.QueryEscape("http://127.0.0.1:8787/console/")) && !strings.Contains(body, "127.0.0.1") {
		t.Fatalf("应携带登记的回跳地址: %s", body)
	}
	if rec2 := send(h, http.MethodGet, "/admin/v1/auth/whoami", cookie, ""); rec2.Code != 401 {
		t.Fatalf("SSO 登出后本地会话必须失效: %d", rec2.Code)
	}
	items, _, err := store.ListAdminAudit(context.Background(), db, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	attributed := false
	for _, a := range items {
		if a.Action == "auth.logout.sso" && a.ActorKind == "federated" && a.ActorName == "张三" {
			attributed = true
		}
	}
	if !attributed {
		t.Fatalf("SSO 登出审计必须归因到会话主体: %+v", items)
	}
}

// TestFederation_联邦登录落审计 覆盖回调 GET 不经变更审计时仍留登录结果。
func TestFederation_联邦登录落审计(t *testing.T) {
	db := openDB(t)
	idp := newTestIdP(t)
	h, _ := newFederatedAdmin(t, db, "tok", idp.rp())
	federatedLogin(t, h, idp)
	items, _, err := store.ListAdminAudit(context.Background(), db, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range items {
		if a.Action == "auth.oidc.login" && a.TargetID == "jinguan-sub-1" && a.Outcome == "ok" {
			found = true
		}
	}
	if !found {
		t.Fatalf("成功登录必须写入 auth.oidc.login 审计: %+v", items)
	}
}

// TestFederation_无管理员角色被拒并审计 覆盖 HTTP 全链路拒绝与审计原因码。
func TestFederation_无管理员角色被拒并审计(t *testing.T) {
	db := openDB(t)
	idp := newTestIdP(t)
	idp.roles = nil
	svc, err := auth.NewAdminService(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	h := admin.New(context.Background(), admin.Deps{
		DB: db, Token: "tok", AdminAuth: svc, Federated: idp.rp(),
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	start := send(h, http.MethodGet, "/admin/v1/auth/oidc/start", nil, "")
	loc, err := url.Parse(start.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	idp.nonce = loc.Query().Get("nonce")
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet,
		"/admin/v1/auth/oidc/callback?code=c1&state="+url.QueryEscape(loc.Query().Get("state")), nil)
	for _, c := range start.Result().Cookies() {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound || !strings.Contains(rec.Header().Get("Location"), "oidc_error=") {
		t.Fatalf("无管理员角色必须拒绝: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	var principals int
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM admin_principals`).Scan(&principals); err != nil || principals != 0 {
		t.Fatalf("拒绝路径不得建主体: %d %v", principals, err)
	}
	items, _, err := store.ListAdminAudit(context.Background(), db, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range items {
		if a.Action == "auth.oidc.login" && a.Outcome == "denied" &&
			a.TargetID == "jinguan-sub-1" && strings.Contains(a.Detail, "subject_not_allowed_or_disabled") {
			found = true
		}
	}
	if !found {
		t.Fatalf("角色拒绝必须带原因码审计: %+v", items)
	}
}

// TestFederation_无会话SSO登出仍可清理并留审计 覆盖会话已过期时的免鉴权登出。
func TestFederation_无会话SSO登出仍可清理并留审计(t *testing.T) {
	db := openDB(t)
	idp := newTestIdP(t)
	h, _ := newFederatedAdmin(t, db, "tok", idp.rp())
	rec := send(h, http.MethodPost, "/admin/v1/auth/logout/sso", nil, "")
	if rec.Code != 200 {
		t.Fatalf("无会话 SSO 登出应 200: %d %s", rec.Code, rec.Body.String())
	}
	items, _, err := store.ListAdminAudit(context.Background(), db, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range items {
		if a.Action == "auth.logout.sso" && a.ActorKind == "login" && a.ActorName == "anonymous" {
			found = true
		}
	}
	if !found {
		t.Fatalf("无会话登出仍须留审计: %+v", items)
	}
}

// TestFederation_登录入口按IP限流 覆盖匿名 start 被刷穿时返回 429。
func TestFederation_登录入口按IP限流(t *testing.T) {
	db := openDB(t)
	idp := newTestIdP(t)
	lim := ratelimit.New()
	for range 30 {
		if ok, _ := lim.Allow("login:192.0.2.1", 30); !ok {
			t.Fatal("预热限流器失败")
		}
	}
	svc, err := auth.NewAdminService(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	h := admin.New(context.Background(), admin.Deps{
		DB: db, Token: "tok", AdminAuth: svc, Federated: idp.rp(), LoginLimiter: lim,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	rec := send(h, http.MethodGet, "/admin/v1/auth/oidc/start", nil, "")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("超限 start 必须 429: %d %s", rec.Code, rec.Body.String())
	}
}

// TestFederation_过期state在写入时清理 覆盖只发起不回调也不会无限堆积。
func TestFederation_过期state在写入时清理(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	cutoff := now.Add(-10 * time.Minute)
	oldAt := now.Add(-20 * time.Minute)
	if err := store.SaveOIDCState(ctx, db, "old-hash", "v", "n",
		oldAt.Format(time.RFC3339Nano), cutoff.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveOIDCState(ctx, db, "new-hash", "v", "n",
		now.Format(time.RFC3339Nano), cutoff.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	var oldCount, newCount int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM oidc_pending_states WHERE state_hash='old-hash'`).Scan(&oldCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM oidc_pending_states WHERE state_hash='new-hash'`).Scan(&newCount); err != nil {
		t.Fatal(err)
	}
	if oldCount != 0 || newCount != 1 {
		t.Fatalf("过期行应随写入清理: old=%d new=%d", oldCount, newCount)
	}
}

// TestFederation_SecureCookie跟随部署配置 覆盖反代终止 TLS 时按显式配置置 Secure。
func TestFederation_SecureCookie跟随部署配置(t *testing.T) {
	db := openDB(t)
	idp := newTestIdP(t)
	svc, err := auth.NewAdminService(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	h := admin.New(context.Background(), admin.Deps{
		DB: db, Token: "tok", AdminAuth: svc, Federated: idp.rp(), SecureCookies: true,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	rec := send(h, http.MethodGet, "/admin/v1/auth/oidc/start", nil, "")
	stateSecure := false
	for _, c := range rec.Result().Cookies() {
		if c.Name == "muniu_oidc_state" {
			stateSecure = c.Secure
		}
	}
	if !stateSecure {
		t.Fatal("显式开启后 state cookie 必须带 Secure")
	}
	cookie, _ := federatedLogin(t, h, idp)
	if !cookie.Secure {
		t.Fatal("显式开启后会话 cookie 必须带 Secure")
	}
}

// TestFederation_Keycloak不可达不阻断令牌轨 覆盖控制面失联时数据面照跑、令牌轨零回归。
func TestFederation_锦官不可达不阻断令牌(t *testing.T) {
	db := openDB(t)
	dead := httptest.NewServer(http.NotFoundHandler())
	issuer := dead.URL
	dead.Close()
	rp := oidc.New(oidc.Config{Issuer: issuer, ClientID: "muniu-console",
		RedirectURL: "http://127.0.0.1:8787/admin/v1/auth/oidc/callback"}, nil)
	h, _ := newFederatedAdmin(t, db, "shared-token", rp)

	rec := send(h, http.MethodGet, "/admin/v1/auth/oidc/start", nil, "")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("权威不可达应 502,实际 %d", rec.Code)
	}
	rec = send(h, http.MethodGet, "/admin/v1/auth/oidc/callback?code=c&state=s", nil, "")
	if rec.Code != http.StatusFound || !strings.Contains(rec.Header().Get("Location"), "oidc_error=") {
		t.Fatalf("回调失败应带错误回登录页: %d %s", rec.Code, rec.Header().Get("Location"))
	}

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/admin/v1/auth/whoami", nil)
	req.Header.Set("Authorization", "Bearer shared-token")
	recT := httptest.NewRecorder()
	h.ServeHTTP(recT, req)
	if recT.Code != 200 || !strings.Contains(recT.Body.String(), `"kind":"token"`) {
		t.Fatalf("Keycloak不可达时令牌轨必须照常: %d %s", recT.Code, recT.Body.String())
	}
}

func TestFederation_仅联邦也挂载管理面(t *testing.T) {
	db := openDB(t)
	idp := newTestIdP(t)
	h, _ := newFederatedAdmin(t, db, "", idp.rp())
	rec := send(h, http.MethodGet, "/admin/v1/auth/whoami", nil, "")
	if rec.Code != 401 {
		t.Fatalf("未认证应 401,实际 %d", rec.Code)
	}
}

func TestFederation_未配置时关闭态(t *testing.T) {
	db := openDB(t)
	h := admin.New(context.Background(), admin.Deps{
		DB: db, Token: "tok", Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	rec := send(h, http.MethodGet, "/admin/v1/auth/federation", nil, "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"enabled":false`) {
		t.Fatalf("未配置时应 enabled:false: %d %s", rec.Code, rec.Body.String())
	}
	rec = send(h, http.MethodGet, "/admin/v1/auth/oidc/start", nil, "")
	if rec.Code != 404 {
		t.Fatalf("未配置时 start 应 404,实际 %d", rec.Code)
	}
}
