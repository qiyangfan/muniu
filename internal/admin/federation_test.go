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
	"github.com/solvela/muniu/internal/store"
)

// testIdP 测试用锦官权威:发现文档 + JWKS + token 端点;nonce/sub/name 由用例回填。
type testIdP struct {
	srv   *httptest.Server
	key   *rsa.PrivateKey
	nonce string
	sub   string
	name  string
}

func newTestIdP(t *testing.T) *testIdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	idp := &testIdP{key: key, sub: "jinguan-sub-1", name: "张三"}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": idp.srv.URL, "authorization_endpoint": idp.srv.URL + "/authorize",
			"token_endpoint": idp.srv.URL + "/token", "jwks_uri": idp.srv.URL + "/jwks",
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

func TestFederation_三轨并存与联邦归因(t *testing.T) {
	db := openDB(t)
	idp := newTestIdP(t)
	h, svc := newFederatedAdmin(t, db, "shared-token", idp.rp())
	if _, err := svc.Create(context.Background(), "localadmin", "pw-123456"); err != nil {
		t.Fatal(err)
	}

	// 联邦入口公开可用,登录页据此决定是否渲染按钮
	rec := send(h, http.MethodGet, "/admin/v1/auth/federation", nil, "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"enabled":true`) {
		t.Fatalf("federation 探测应 enabled: %d %s", rec.Code, rec.Body.String())
	}

	cookie, _ := federatedLogin(t, h, idp)
	rec = send(h, http.MethodGet, "/admin/v1/auth/whoami", cookie, "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"kind":"federated"`) ||
		!strings.Contains(rec.Body.String(), "张三") {
		t.Fatalf("联邦会话 whoami 应归因 federated/锦官实名: %d %s", rec.Code, rec.Body.String())
	}

	// 令牌轨
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/admin/v1/auth/whoami", nil)
	req.Header.Set("Authorization", "Bearer shared-token")
	recT := httptest.NewRecorder()
	h.ServeHTTP(recT, req)
	if recT.Code != 200 || !strings.Contains(recT.Body.String(), `"kind":"token"`) {
		t.Fatalf("令牌轨零回归: %d %s", recT.Code, recT.Body.String())
	}

	// 本地会话轨
	recL := send(h, http.MethodPost, "/admin/v1/auth/login", nil,
		`{"username":"localadmin","password":"pw-123456"}`)
	if recL.Code != 200 || !strings.Contains(recL.Body.String(), `"kind":"session"`) {
		t.Fatalf("本地登录应保留: %d %s", recL.Code, recL.Body.String())
	}

	// 联邦变更动作审计归因
	recP := send(h, http.MethodPatch, "/admin/v1/admins/"+federatedID(t, db), cookie, `{"disabled":false}`)
	if recP.Code != 200 {
		t.Fatalf("联邦管理员变更应获准: %d %s", recP.Code, recP.Body.String())
	}
	items, _, err := store.ListAdminAudit(context.Background(), db, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range items {
		if a.ActorKind == "federated" && a.ActorName == "张三" {
			found = true
		}
	}
	if !found {
		t.Fatalf("审计应含 federated/张三: %+v", items)
	}
}

func federatedID(t *testing.T, db *sql.DB) string {
	t.Helper()
	u, err := store.GetAdminUserBySubject(context.Background(), db, "jinguan-sub-1")
	if err != nil {
		t.Fatal(err)
	}
	return u.ID
}

func TestFederation_同一sub恒映射同一管理员(t *testing.T) {
	db := openDB(t)
	idp := newTestIdP(t)
	h, _ := newFederatedAdmin(t, db, "tok", idp.rp())
	federatedLogin(t, h, idp)
	federatedLogin(t, h, idp)

	users, err := store.ListAdminUsers(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	fed := 0
	for _, u := range users {
		if u.Origin == "federated" {
			fed++
			if u.ExternalSubject != "jinguan-sub-1" || u.Username != "张三" {
				t.Fatalf("联邦影子账号映射错误: %+v", u)
			}
		}
	}
	if fed != 1 {
		t.Fatalf("同一 sub 只应有一个本地管理员,实际 %d", fed)
	}
}

func TestFederation_本地同名不抢占联邦账号(t *testing.T) {
	db := openDB(t)
	idp := newTestIdP(t)
	h, svc := newFederatedAdmin(t, db, "tok", idp.rp())
	if _, err := svc.Create(context.Background(), "张三", "pw-123456"); err != nil {
		t.Fatal(err)
	}
	federatedLogin(t, h, idp)
	fed, err := store.GetAdminUserBySubject(context.Background(), db, "jinguan-sub-1")
	if err != nil {
		t.Fatal(err)
	}
	if fed.Username == "张三" {
		t.Fatal("联邦账号不得占用本地同名用户名")
	}
	local, err := store.GetAdminUser(context.Background(), db, "张三")
	if err != nil || local.Origin != "local" {
		t.Fatalf("本地账号应保持原样: %+v %v", local, err)
	}
}

func TestFederation_停用账号拒绝登录(t *testing.T) {
	db := openDB(t)
	idp := newTestIdP(t)
	h, svc := newFederatedAdmin(t, db, "tok", idp.rp())
	federatedLogin(t, h, idp)
	id := federatedID(t, db)
	disabled := true
	if err := svc.Patch(context.Background(), id, &disabled, ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.LoginFederated(context.Background(), "jinguan-sub-1", "张三"); err == nil {
		t.Fatal("停用的联邦账号必须拒绝")
	}
	// 本地账密轨对其同样不可用(空口令哈希)
	if _, _, err := svc.Login(context.Background(), "张三", ""); err == nil {
		t.Fatal("联邦账号不得经本地账密登录")
	}
}

func TestFederation_锦官不可达不阻断本地与令牌(t *testing.T) {
	db := openDB(t)
	dead := httptest.NewServer(http.NotFoundHandler())
	issuer := dead.URL
	dead.Close()
	rp := oidc.New(oidc.Config{Issuer: issuer, ClientID: "muniu-console",
		RedirectURL: "http://127.0.0.1:8787/admin/v1/auth/oidc/callback"}, nil)
	h, svc := newFederatedAdmin(t, db, "shared-token", rp)
	if _, err := svc.Create(context.Background(), "localadmin", "pw-123456"); err != nil {
		t.Fatal(err)
	}

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
		t.Fatalf("锦官不可达时令牌轨必须照常: %d %s", recT.Code, recT.Body.String())
	}
	recL := send(h, http.MethodPost, "/admin/v1/auth/login", nil,
		`{"username":"localadmin","password":"pw-123456"}`)
	if recL.Code != 200 {
		t.Fatalf("锦官不可达时本地账密必须照常: %d %s", recL.Code, recL.Body.String())
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
