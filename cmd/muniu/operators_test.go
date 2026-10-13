package main

import (
	"bytes"
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
	"github.com/solvela/muniu/internal/metering"
	"github.com/solvela/muniu/internal/store"
)

func TestAdminsCLI_全生命周期与纯账号挂载(t *testing.T) {
	cfg := &config.Config{}
	cfg.Storage.SQLite.Path = filepath.Join(t.TempDir(), "admins.db")
	var out, errout bytes.Buffer
	for _, args := range [][]string{{"add", "--username", "alice"}, {"list"}, {"disable", "alice"}, {"enable", "alice"}, {"passwd", "alice"}} {
		if code := adminsMain(cfg, args, strings.NewReader("password123\n"), &out, &errout); code != 0 {
			t.Fatalf("%v: %d %s", args, code, errout.String())
		}
	}
	if strings.Contains(out.String(), "password123") {
		t.Fatal("CLI 泄漏口令")
	}
	db, err := store.Open(cfg.Storage.SQLite.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	svc, err := auth.NewAdminService(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Login("alice", "password123"); err != nil {
		t.Fatal(err)
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
	h, routes, cleanup, err := assemble(loaded, "", slog.New(slog.NewTextHandler(io.Discard, nil)))
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
		t.Fatal("无 token 活跃账号未挂载")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/admin/v1/auth/whoami", nil))
	if w.Header().Get("X-Muniu-Request-Id") == "" {
		t.Fatal("管理面 request_id 未接入")
	}
	if w.Code != http.StatusUnauthorized {
		t.Fatal(w.Code)
	}
}

func TestImagePricing_热更与脏目录保持(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "prices.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	cfg := &config.Config{ImagePricing: map[string]config.ImagePrice{"paint": {Currency: "CNY", Default: "0.35", Tiers: map[string]string{"1024|hd": "0.3"}}}}
	dir, _, update, err := assembleImagePricing(db, cfg, "CNY", 0)
	if err != nil {
		t.Fatal(err)
	}
	if dir.Get("paint").DefaultCredits != 350000000 {
		t.Fatal("静态价")
	}
	r := &store.ImagePricingRecord{Alias: "paint", Currency: "CNY", DefaultCredits: 500, Tiers: map[string]int64{}}
	if err := update("paint", r); err != nil {
		t.Fatal(err)
	}
	if dir.Get("paint").DefaultCredits != 500 {
		t.Fatal("未热更")
	}
	if err := update("paint", nil); err != nil {
		t.Fatal(err)
	}
	if dir.Get("paint").DefaultCredits != 350000000 {
		t.Fatal("未回落")
	}
	if err := update("paint", nil); err != store.ErrNotFound {
		t.Fatal("重复删除未404", err)
	}
	bad := &store.ImagePricingRecord{Alias: "broken", Currency: "CNY", DefaultCredits: 500, Tiers: map[string]int64{"bad": 1}}
	if err := update("broken", bad); err == nil {
		t.Fatal("非法整单未拒绝")
	}
	if _, err := db.Exec(`INSERT INTO image_pricing VALUES('broken','CNY',500,'{"bad":"0.2"}','now')`); err != nil {
		t.Fatal(err)
	}
	if err := update("paint", r); err == nil {
		t.Fatal("脏目录未拒绝重建")
	}
	if dir.Get("paint").DefaultCredits != 350000000 {
		t.Fatal("旧目录被破坏")
	}
	if _, err := db.Exec(`DELETE FROM image_pricing WHERE alias='broken'`); err != nil {
		t.Fatal(err)
	}
	q := metering.ImagePrice{Currency: "USD", DefaultCredits: 100, Tiers: map[string]int64{"1024|hd": 50}}
	settled, err := metering.SettleImagePrice(q, "CNY", 7200)
	if err != nil || settled.DefaultCredits != 720 || settled.Tiers["1024|hd"] != 360 {
		t.Fatal("本位币错误", settled, err)
	}
}

func TestAdminsCLI_空密码不得报告修改成功(t *testing.T) {
	cfg := &config.Config{}
	cfg.Storage.SQLite.Path = filepath.Join(t.TempDir(), "empty.db")
	var out, errout bytes.Buffer
	if code := adminsMain(cfg, []string{"add", "--username", "alice"}, strings.NewReader("password123\n"), &out, &errout); code != 0 {
		t.Fatal(code, errout.String())
	}
	for _, input := range []string{"", "\n"} {
		if code := adminsMain(cfg, []string{"passwd", "alice"}, strings.NewReader(input), &out, &errout); code == 0 {
			t.Fatal("空密码被报告为修改成功")
		}
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
	h, _, cleanup, err := assemble(cfg, "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	key, _, err := auth.CreateKey(db, "default", "images", 60, auth.KeyOpts{})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/v1/images/generations", strings.NewReader(`{"model":"paint","prompt":"x"}`))
	r.Header.Set("Authorization", "Bearer "+key)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var currency string
		err := db.QueryRow(`SELECT currency FROM usage_logs LIMIT 1`).Scan(&currency)
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

func TestAdminPassword_输入字节上限(t *testing.T) {
	for _, n := range []int{4096, 4097, 100000} {
		password, err := readAdminPassword(strings.NewReader(strings.Repeat("x", n)+"\n"), io.Discard)
		if n == 4096 {
			if err != nil || len(password) != n {
				t.Fatal("边界合法密码被拒绝", err)
			}
		} else if err == nil {
			t.Fatal("超长输入被接受", n)
		}
	}
}
