package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/solvela/muniu/internal/auth"
	"github.com/solvela/muniu/internal/config"
	"github.com/solvela/muniu/internal/store"
)

// TestAdminsCLI_主体启停与纯主体挂载 覆盖 list/disable/enable 按 subject 生效、
// 停用撤销会话、本地创建/改密子命令已被拒绝,以及无令牌仅凭活跃主体挂载管理面。
func TestAdminsCLI_主体启停与纯主体挂载(t *testing.T) {
	cfg := &config.Config{}
	cfg.Storage.SQLite.Path = filepath.Join(t.TempDir(), "admins.db")
	db, err := store.Open(context.Background(), cfg.Storage.SQLite.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	svc, err := auth.NewAdminService(context.Background(), db, nil)
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := svc.LoginFederated(context.Background(), "hufu-sub-alice", "alice")
	if err != nil {
		t.Fatal(err)
	}
	var out, errout bytes.Buffer
	if code := adminsMain(cfg, []string{"list"}, &out, &errout); code != 0 ||
		!strings.Contains(out.String(), "hufu-sub-alice\talice\tactive") {
		t.Fatalf("list: %d %s %s", code, out.String(), errout.String())
	}
	if code := adminsMain(cfg, []string{"disable", "--subject", "hufu-sub-alice"}, &out, &errout); code != 0 {
		t.Fatalf("disable: %d %s", code, errout.String())
	}
	if _, err := svc.Session(context.Background(), token); err == nil {
		t.Fatal("CLI 停用必须撤销会话")
	}
	if code := adminsMain(cfg, []string{"enable", "--subject", "hufu-sub-alice"}, &out, &errout); code != 0 {
		t.Fatalf("enable: %d %s", code, errout.String())
	}
	// 本地账密子命令删除后,未知子命令必须以用法错误退出
	for _, args := range [][]string{{"add", "--username", "bob"}, {"passwd", "alice"}, {"disable", "alice"}, {}} {
		if code := adminsMain(cfg, args, &out, &errout); code != 2 {
			t.Fatalf("%v 应退出码 2,实际 %d", args, code)
		}
	}
	t.Setenv("MUNIU_ADMIN_TOKEN", "")
	// 装配其余字段使用真实配置加载缺省，不伪造超时等参数。
	loaded, err := config.Load("../../muniu.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	loaded.Storage.SQLite.Path = cfg.Storage.SQLite.Path
	loaded.Providers = nil
	loaded.ModelRoutes = nil
	loaded.Pricing = nil
	// 不带数据面拓扑的配置直接装配，仅验证管理面挂载。
	h, routes, cleanup, err := assemble(context.Background(), loaded, "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	found := false
	for _, route := range routes {
		if route == "GET /admin/v1/auth/whoami" {
			found = true
		}
	}
	if !found {
		t.Fatal("无 token 活跃主体未挂载")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), "GET", "/admin/v1/auth/whoami", nil))
	if w.Header().Get("X-Muniu-Request-Id") == "" {
		t.Fatal("管理面 request_id 未接入")
	}
	if w.Code != http.StatusUnauthorized {
		t.Fatal(w.Code)
	}
}

// TestAssemble_旧本地管理员库拒绝启动 覆盖零存量重建的启动闸:含本地账密行必须报错而非静默丢行。
func TestAssemble_旧本地管理员库拒绝启动(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.db")
	db, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `INSERT INTO admin_users(id,username,password_hash,status,created_at)
		VALUES('u1','legacy','$argon2id$x','active','2026-11-20T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	t.Setenv("MUNIU_ADMIN_TOKEN", "tok")
	loaded, err := config.Load("../../muniu.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	loaded.Storage.SQLite.Path = path
	loaded.Providers = nil
	loaded.ModelRoutes = nil
	loaded.Pricing = nil
	_, _, cleanup, err := assemble(context.Background(), loaded, "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if cleanup != nil {
		cleanup()
	}
	if err == nil || !strings.Contains(err.Error(), "legacy") {
		t.Fatalf("旧本地管理员库必须拒绝启动并指名行: %v", err)
	}
}

func TestImages_无价目仍使用网关本位币(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	dbPath := filepath.Join(dir, "currency.db")
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, `{"data":[]}`) }))
	defer up.Close()
	t.Setenv("REVIEW_IMAGE_KEY", "fixture")
	t.Setenv("MUNIU_ADMIN_TOKEN", "")
	raw := fmt.Sprintf("storage:\n  sqlite:\n    path: %q\nmetering:\n  settlement: USD\n  flush_batch: 1\nproviders:\n  - name: fake\n    adapter: openai\n    weight: 1\n    base_url: %q\n    keys: [{env: REVIEW_IMAGE_KEY}]\nmodel_routes:\n  paint: [{provider: fake, model: paint, priority: 0}]\n", dbPath, up.URL)
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	h, _, cleanup, err := assemble(context.Background(), cfg, "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	db, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	key, _, err := auth.CreateKey(context.Background(), db, "default", "images", 60, auth.KeyOpts{})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequestWithContext(context.Background(), "POST", "/v1/images/generations", strings.NewReader(`{"model":"paint","prompt":"x"}`))
	r.Header.Set("Authorization", "Bearer "+key)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var currency string
		err := db.QueryRowContext(context.Background(), `SELECT currency FROM usage_logs LIMIT 1`).Scan(&currency)
		if err == nil {
			if currency != "USD" {
				t.Fatalf("无价目行币种=%s，应为USD", currency)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("计量行未落库")
}
