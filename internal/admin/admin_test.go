// 管理面契约测试:token 关闭/鉴权失败、租户与 key CRUD、usage 聚合参数校验、providers 拓扑、缓存统计空态。
package admin_test

import (
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/solvela/muniu/internal/admin"
	"github.com/solvela/muniu/internal/auth"
	"github.com/solvela/muniu/internal/config"
	"github.com/solvela/muniu/internal/keypool"
	"github.com/solvela/muniu/internal/store"
)

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "admin.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func newAdmin(t *testing.T, db *sql.DB, token string, pools map[string]*keypool.Pool) http.Handler {
	t.Helper()
	h := admin.New(admin.Deps{
		DB:        db,
		Token:     token,
		Providers: []config.Provider{{Name: "p1", Adapter: "openai", BaseURL: "https://x/v1", Keys: []config.KeyRef{{Env: "K1", Weight: 5}}}},
		Pools:     pools,
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if h == nil {
		t.Fatal("token 非空时 Handler 不应为 nil")
	}
	return h
}

func call(t *testing.T, h http.Handler, method, path, token, body string) (int, string) {
	t.Helper()
	var rd *strings.Reader
	if body == "" {
		rd = strings.NewReader("")
	} else {
		rd = strings.NewReader(body)
	}
	req, _ := http.NewRequest(method, path, rd)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	b, _ := io.ReadAll(rec.Body)
	return rec.Code, string(b)
}

func TestAdmin_token缺失整体关闭(t *testing.T) {
	db := openDB(t)
	if h := admin.New(admin.Deps{DB: db, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}); h != nil {
		t.Fatal("token 缺失必须返回 nil(管理面关闭)")
	}
}

func TestAdmin_鉴权失败401(t *testing.T) {
	db := openDB(t)
	h := newAdmin(t, db, "right-token", nil)
	code, body := call(t, h, "GET", "/admin/v1/tenants", "wrong-token", "")
	if code != 401 || !strings.Contains(body, "auth_failed") {
		t.Fatalf("鉴权失败应 401: %d %s", code, body)
	}
}

func TestAdmin_租户CRUD全链(t *testing.T) {
	db := openDB(t)
	h := newAdmin(t, db, "tok", nil)

	// 创建
	code, body := call(t, h, "POST", "/admin/v1/tenants", "tok", `{"name":"acme"}`)
	if code != 200 {
		t.Fatalf("创建失败: %d %s", code, body)
	}
	var created struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal([]byte(body), &created)

	// 列表含 default + acme
	code, body = call(t, h, "GET", "/admin/v1/tenants", "tok", "")
	if code != 200 || !strings.Contains(body, "acme") || !strings.Contains(body, "default") {
		t.Fatalf("列表不符: %d %s", code, body)
	}

	// 更新:禁用 + 配额
	quota := int64(5000)
	patch, _ := json.Marshal(map[string]any{"status": "disabled", "daily_quota_micro": quota})
	code, body = call(t, h, "PATCH", "/admin/v1/tenants/"+created.ID, "tok", string(patch))
	if code != 200 {
		t.Fatalf("更新失败: %d %s", code, body)
	}
	code, _ = call(t, h, "PATCH", "/admin/v1/tenants/no-such", "tok", string(patch))
	if code != 404 {
		t.Fatalf("不存在租户应 404: %d", code)
	}

	// 状态校验
	code, _ = call(t, h, "PATCH", "/admin/v1/tenants/"+created.ID, "tok", `{"status":"weird"}`)
	if code != 400 {
		t.Fatalf("非法 status 应 400: %d", code)
	}
}

func TestAdmin_keysCRUD全链(t *testing.T) {
	db := openDB(t)
	h := newAdmin(t, db, "tok", nil)

	code, body := call(t, h, "POST", "/admin/v1/keys", "tok", `{"tenant":"default","name":"api-key","rpm_limit":120}`)
	if code != 200 {
		t.Fatalf("签发失败: %d %s", code, body)
	}
	var created struct {
		Key    string `json:"key"`
		Prefix string `json:"prefix"`
	}
	_ = json.Unmarshal([]byte(body), &created)
	if !strings.HasPrefix(created.Key, "mk-") {
		t.Fatalf("明文形态不符: %q", created.Key)
	}

	code, body = call(t, h, "GET", "/admin/v1/keys", "tok", "")
	if code != 200 || !strings.Contains(body, created.Prefix) || strings.Contains(body, created.Key) {
		t.Fatalf("列表不得泄露明文且应含 prefix: %s", body)
	}

	code, body = call(t, h, "DELETE", "/admin/v1/keys/"+created.Prefix, "tok", "")
	if code != 200 {
		t.Fatalf("吊销失败: %d %s", code, body)
	}
	code, _ = call(t, h, "DELETE", "/admin/v1/keys/"+created.Prefix, "tok", "")
	if code != 404 {
		t.Fatalf("重复吊销应 404: %d", code)
	}
}

func TestAdmin_usage参数校验与聚合(t *testing.T) {
	db := openDB(t)
	h := newAdmin(t, db, "tok", nil)

	code, body := call(t, h, "GET", "/admin/v1/usage?group_by=bogus", "tok", "")
	if code != 400 {
		t.Fatalf("非法 group_by 应 400: %d %s", code, body)
	}
	code, body = call(t, h, "GET", "/admin/v1/usage?group_by=day", "tok", "")
	if code != 200 {
		t.Fatalf("聚合失败: %d %s", code, body)
	}
	// 空数据返回空数组而非 null
	if strings.Contains(body, "\"points\":null") {
		t.Fatalf("空数据应为空数组: %s", body)
	}
}

func TestAdmin_providers拓扑与池状态(t *testing.T) {
	db := openDB(t)
	pool := keypool.NewPool([]keypool.KeyInput{{ID: "K1", Value: "v", Weight: 5}})
	h := newAdmin(t, db, "tok", map[string]*keypool.Pool{"p1": pool})

	code, body := call(t, h, "GET", "/admin/v1/providers", "tok", "")
	if code != 200 {
		t.Fatal(body)
	}
	if !strings.Contains(body, `"state":"healthy"`) || !strings.Contains(body, `"weight":5`) {
		t.Fatalf("拓扑/池状态不符: %s", body)
	}
}

func TestAdmin_缓存统计空态(t *testing.T) {
	db := openDB(t)
	h := admin.New(admin.Deps{
		DB: db, Token: "tok", Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	code, body := call(t, h, "GET", "/admin/v1/cache/stats", "tok", "")
	if code != 200 || !strings.Contains(body, `"enabled":false`) {
		t.Fatalf("未接缓存统计应返回空态: %d %s", code, body)
	}
}

// 吊销 key 必须即时清空进程内验证缓存(Deps.Verifier 接线):未清前已缓存的 key 在 TTL 内仍可放行。
func TestAdmin_吊销key即时失效验证缓存(t *testing.T) {
	db := openDB(t)
	plaintext, rec, err := auth.CreateKey(db, "default", "revoke-pin", 60)
	if err != nil {
		t.Fatal(err)
	}
	verifier := auth.NewVerifier(db, auth.Options{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	h := admin.New(admin.Deps{
		DB: db, Token: "t",
		Providers: []config.Provider{{Name: "p1", Adapter: "openai", BaseURL: "https://x/v1", Keys: []config.KeyRef{{Env: "K1", Weight: 5}}}},
		Verifier:  verifier,
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	probe := func() int {
		pr := httptest.NewRecorder()
		req, _ := http.NewRequest("POST", "/v1/chat/completions", nil)
		req.Header.Set("Authorization", "Bearer "+plaintext)
		verifier.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })).ServeHTTP(pr, req)
		return pr.Code
	}
	if probe() != 200 {
		t.Fatal("吊销前鉴权应通过(并预热缓存)")
	}
	code, _ := call(t, h, "DELETE", "/admin/v1/keys/"+rec.Prefix, "t", "")
	if code != 200 {
		t.Fatalf("吊销应 200: %d", code)
	}
	if got := probe(); got != 401 {
		t.Fatalf("吊销后必须即时 401(缓存已清), 实际 %d", got)
	}
}
