// OAuth 账号管理端点契约:未鉴权 401、凭据脱敏、引用删除拒绝、state 重放拒绝。
package admin_test

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/solvela/muniu/internal/admin"
	"github.com/solvela/muniu/internal/config"
	"github.com/solvela/muniu/internal/keypool"
	"github.com/solvela/muniu/internal/oauth"
	"github.com/solvela/muniu/internal/store"
)

func newOAuthAdmin(t *testing.T, db *sql.DB, token string, topo func() ([]config.Provider, map[string]*keypool.Pool), flow *oauth.Flow) http.Handler {
	t.Helper()
	h := admin.New(admin.Deps{
		DB:        db,
		Token:     token,
		Providers: []config.Provider{},
		Topology:  topo,
		OAuthFlow: flow,
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if h == nil {
		t.Fatal("token 非空时 Handler 不应为 nil")
	}
	return h
}

func TestOAuthAccounts_未鉴权401与凭据脱敏(t *testing.T) {
	db := openDB(t)
	h := newOAuthAdmin(t, db, "tok", nil, nil)

	// 未鉴权 401(与其他 admin 端点同语义)
	if code, _ := call(t, h, "GET", "/admin/v1/oauth/accounts", "", ""); code != 401 {
		t.Fatalf("未鉴权应 401, got %d", code)
	}

	// 录入 → 列表脱敏:响应含掩码形态,不含凭据原文
	if code, body := call(t, h, "POST", "/admin/v1/oauth/accounts", "tok",
		`{"id":"acc-1","access_token":"sk-secret-value-1234567890","refresh_token":"rt-secret-value-1234567890"}`); code != 200 {
		t.Fatalf("录入应 200, got %d: %s", code, body)
	}
	if code, _ := call(t, h, "POST", "/admin/v1/oauth/accounts", "tok",
		`{"id":"acc-1","access_token":"x","refresh_token":"y"}`); code != 409 {
		t.Fatalf("重复 id 应 409, got %d", code)
	}
	if code, body := call(t, h, "POST", "/admin/v1/oauth/accounts", "tok", `{"id":"acc-2"}`); code != 400 {
		t.Fatalf("缺凭据应 400, got %d: %s", code, body)
	}
	code, body := call(t, h, "GET", "/admin/v1/oauth/accounts", "tok", "")
	if code != 200 {
		t.Fatalf("列表应 200, got %d", code)
	}
	if strings.Contains(body, "sk-secret-value-1234567890") || strings.Contains(body, "rt-secret-value-1234567890") {
		t.Fatalf("列表泄漏凭据原文: %s", body)
	}
	if !strings.Contains(body, "sk-sec…") || !strings.Contains(body, "plaintext_disclosure") {
		t.Fatalf("列表缺脱敏形态或明文落库披露: %s", body)
	}
}

func TestOAuthAccounts_引用删除拒绝(t *testing.T) {
	db := openDB(t)
	now := time.Now()
	if err := store.CreateOAuthAccount(db, store.OAuthAccountRow{
		ID: "acc-1", ProviderType: "claude", Status: store.OAuthStatusActive,
		AccessToken: "at", RefreshToken: "rt", ExpiresAt: now.Add(time.Hour),
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("建账号: %v", err)
	}
	referencing := true
	topo := func() ([]config.Provider, map[string]*keypool.Pool) {
		if !referencing {
			return []config.Provider{}, nil
		}
		return []config.Provider{{
			Name: "claude", Adapter: "anthropic", Auth: "oauth",
			Keys: []config.KeyRef{{Account: "acc-1", Weight: 1}},
		}}, nil
	}
	h := newOAuthAdmin(t, db, "tok", topo, nil)

	if code, body := call(t, h, "DELETE", "/admin/v1/oauth/accounts/acc-1", "tok", ""); code != 409 {
		t.Fatalf("引用中删除应 409, got %d: %s", code, body)
	}
	if code, body := call(t, h, "DELETE", "/admin/v1/oauth/accounts/nope", "tok", ""); code != 404 {
		t.Fatalf("未知账号删除应 404, got %d: %s", code, body)
	}
	referencing = false
	if code, _ := call(t, h, "DELETE", "/admin/v1/oauth/accounts/acc-1", "tok", ""); code != 200 {
		t.Fatalf("无引用删除应 200")
	}
}

func TestOAuthAccounts_回调state重放拒绝与授权完成落库(t *testing.T) {
	db := openDB(t)
	now := time.Now()
	// 预建 auth_failed 账号:回调走真正重授权分支(UpdateTokens 成功→mode=reauthorize,
	// 终态恢复 active);NotFound→创建分支由 acc-new 用例钉住
	if err := store.CreateOAuthAccount(db, store.OAuthAccountRow{
		ID: "acc-r1", ProviderType: "claude", Status: store.OAuthStatusAuthFailed, StatusReason: "stale",
		AccessToken: "old", RefreshToken: "rt-old", ExpiresAt: now.Add(time.Hour),
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("建账号: %v", err)
	}
	// 假 token 端点:code=bad-code 模拟上游缺 refresh_token 的降级响应,其余返回完整凭据对
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		if req["code"] == "bad-code" {
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at-x", "expires_in": 3600})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "at-cb", "refresh_token": "rt-cb", "expires_in": 3600,
		})
	}))
	defer tokenSrv.Close()
	flow := oauth.NewFlow(oauth.Config{
		AuthorizeURL: tokenSrv.URL + "/authorize",
		TokenURL:     tokenSrv.URL + "/token",
		ClientID:     "cid",
		CallbackURL:  "http://127.0.0.1:8787/admin/v1/oauth/callback",
	}, tokenSrv.Client(), nil)
	h := newOAuthAdmin(t, db, "tok", nil, flow)

	// 授权流未配置时关闭态(新建无 flow 的实例)
	hNoFlow := newOAuthAdmin(t, openDB(t), "tok", nil, nil)
	if code, _ := call(t, hNoFlow, "POST", "/admin/v1/oauth/authorize", "tok", `{}`); code != 400 {
		t.Fatalf("未配置授权流应 400")
	}

	begin := func(t *testing.T, accountID string) string {
		t.Helper()
		payload := `{}`
		if accountID != "" {
			payload = `{"account_id":"` + accountID + `"}`
		}
		code, body := call(t, h, "POST", "/admin/v1/oauth/authorize", "tok", payload)
		if code != 200 {
			t.Fatalf("授权发起应 200, got %d: %s", code, body)
		}
		var resp struct {
			AuthorizeURL string `json:"authorize_url"`
		}
		if err := json.Unmarshal([]byte(body), &resp); err != nil || resp.AuthorizeURL == "" {
			t.Fatalf("响应缺 authorize_url: %s", body)
		}
		return resp.AuthorizeURL[strings.Index(resp.AuthorizeURL, "state=")+len("state="):]
	}

	// 重授权既有账号 → 回调 → 终态恢复 active + mode=reauthorize
	state := begin(t, "acc-r1")
	code, body := call(t, h, "GET", "/admin/v1/oauth/callback?code=the-code&state="+state, "", "")
	if code != 200 || !strings.Contains(body, `"mode":"reauthorize"`) {
		t.Fatalf("重授权回调应 200 mode=reauthorize, got %d: %s", code, body)
	}
	row, err := store.GetOAuthAccount(db, "acc-r1")
	if err != nil || row.AccessToken != "at-cb" || row.RefreshToken != "rt-cb" ||
		row.Status != store.OAuthStatusActive || row.StatusReason != "" {
		t.Fatalf("重授权落库失真: %+v err=%v", row, err)
	}

	// 同 state 重放 → 400 且零新凭据
	if code, _ := call(t, h, "GET", "/admin/v1/oauth/callback?code=the-code&state="+state, "", ""); code != 400 {
		t.Fatalf("state 重放应 400")
	}

	// 空 account_id 授权新账号 → NotFound→创建分支,mode=created
	state = begin(t, "")
	code, body = call(t, h, "GET", "/admin/v1/oauth/callback?code=the-code&state="+state, "", "")
	if code != 200 || !strings.Contains(body, `"mode":"created"`) {
		t.Fatalf("新账号回调应 200 mode=created, got %d: %s", code, body)
	}

	// 回调响应缺 refresh_token → 502 且凭据不落库(空 RT 入库后刷新链路失真)
	state = begin(t, "acc-nort")
	code, body = call(t, h, "GET", "/admin/v1/oauth/callback?code=bad-code&state="+state, "", "")
	if code != 502 || !strings.Contains(body, "oauth_exchange_failed") {
		t.Fatalf("缺 RT 回调应 502, got %d: %s", code, body)
	}
	if _, gerr := store.GetOAuthAccount(db, "acc-nort"); !errors.Is(gerr, store.ErrOAuthAccountNotFound) {
		t.Fatalf("缺 RT 凭据不得落库: err=%v", gerr)
	}
}

func TestOAuthAccounts_短凭据全掩码与DB故障500(t *testing.T) {
	db := openDB(t)
	h := newOAuthAdmin(t, db, "tok", nil, nil)

	// 短凭据全掩码:前缀回原文即泄漏(maskToken 评审域二 P3);refresh token 仅投影
	// presence 布尔,原文任何形态不得出现在列表响应
	if code, _ := call(t, h, "POST", "/admin/v1/oauth/accounts", "tok",
		`{"id":"acc-s","access_token":"abc","refresh_token":"de"}`); code != 200 {
		t.Fatalf("短凭据录入应 200")
	}
	_, body := call(t, h, "GET", "/admin/v1/oauth/accounts", "tok", "")
	if !strings.Contains(body, "**(3 字符)") {
		t.Fatalf("短凭据应全掩码不得回前缀: %s", body)
	}
	if strings.Contains(body, `"abc"`) || strings.Contains(body, `"de"`) {
		t.Fatalf("列表泄漏凭据原文: %s", body)
	}

	// DB 故障(非冲突)→ 500,不得伪装成 409「账号已存在」误导排障
	if err := db.Close(); err != nil {
		t.Fatalf("关库: %v", err)
	}
	if code, _ := call(t, h, "POST", "/admin/v1/oauth/accounts", "tok",
		`{"id":"acc-x","access_token":"a","refresh_token":"b"}`); code != 500 {
		t.Fatalf("DB 故障应 500, got %d", code)
	}
}

func TestProviders_projection_oauth成员键与装配同源(t *testing.T) {
	// 评审域二 P2 根修钉子:oauth 渠道成员键此前恒用 kr.Env(空串),投影 id 恒空、状态恒
	// unknown——渠道完全不可观测。键规则必须与 buildPool 一致(env 优先,account 兜底)。
	db := openDB(t)
	now := time.Now()
	if err := store.CreateOAuthAccount(db, store.OAuthAccountRow{
		ID: "acc-1", ProviderType: "claude", Status: store.OAuthStatusActive,
		AccessToken: "at", RefreshToken: "rt", ExpiresAt: now.Add(time.Hour),
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("建账号: %v", err)
	}
	pool := keypool.NewPool([]keypool.KeyInput{{ID: "acc-1", Value: "at", Weight: 1}})
	topo := func() ([]config.Provider, map[string]*keypool.Pool) {
		return []config.Provider{{
			Name: "claude", Adapter: "anthropic", Auth: "oauth",
			Keys: []config.KeyRef{{Account: "acc-1", Weight: 1}},
		}}, map[string]*keypool.Pool{"claude": pool}
	}
	h := newOAuthAdmin(t, db, "tok", topo, nil)
	code, body := call(t, h, "GET", "/admin/v1/providers", "tok", "")
	if code != 200 {
		t.Fatalf("providers 应 200, got %d", code)
	}
	if !strings.Contains(body, `"id":"acc-1"`) {
		t.Fatalf("oauth 成员投影 id 应为账号 ID: %s", body)
	}
	if strings.Contains(body, `"state":"unknown"`) {
		t.Fatalf("oauth 成员状态不得恒 unknown: %s", body)
	}
}
