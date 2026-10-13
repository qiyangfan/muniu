// SIGHUP 配置热加载集成测试(sighup-config-reload):
// 合法新配置 → 新请求走新 provider;非法新配置 → 保持旧态继续服务。
// 假上游经 testkit,进程内自发 SIGHUP(signal.Notify 捕获本进程信号)。
package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/solvela/muniu/internal/auth"
	"github.com/solvela/muniu/internal/config"
	"github.com/solvela/muniu/internal/logging"
	"github.com/solvela/muniu/internal/store"
	"github.com/solvela/muniu/internal/testkit"
)

func TestSIGHUP_热加载与保持旧态(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "muniu.yaml")
	dbPath := filepath.Join(dir, "test.db")
	up1 := testkit.StartJSONUpstream(t, 200, testkit.JSONOKBody)
	up2 := testkit.StartJSONUpstream(t, 200, testkit.JSONOKBody)
	t.Setenv("SIGHUP_TEST_KEY_1", "k1")
	t.Setenv("SIGHUP_TEST_KEY_2", "k2")

	writeCfg := func(baseURL, keyEnv string) {
		body := fmt.Sprintf(`server:
  addr: "127.0.0.1:0"
storage:
  sqlite:
    path: %q
providers:
  - name: p1
    adapter: openai
    weight: 1
    base_url: %q
    keys:
      - env: %s
model_routes:
  alias-a:
    - { provider: p1, model: up-a, priority: 0 }
`, dbPath, baseURL, keyEnv)
		if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeCfg(up1.URL(), "SIGHUP_TEST_KEY_1")

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	handler, _, cleanup, err := assemble(cfg, cfgPath, logging.New("error", "text"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	// 数据面 key:经同库签发(装配已开库,第二连接 WAL 共存)
	keyDB, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = keyDB.Close() })
	plaintext, _, kerr := auth.CreateKey(keyDB, "default", "sighup-test", 0, auth.KeyOpts{})
	if kerr != nil {
		t.Fatal(kerr)
	}

	post := func() (int, string) {
		req, _ := http.NewRequest("POST", ts.URL+"/v1/chat/completions",
			strings.NewReader(`{"model":"alias-a","messages":[{"role":"user","content":"hi"}]}`))
		req.Header.Set("Authorization", "Bearer "+plaintext)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	if code, body := post(); code != 200 {
		t.Fatalf("初态应经 up1 服务: %d %s", code, body)
	}
	if up1.RequestCount() != 1 {
		t.Fatalf("初态应触达 up1 一次: %d", up1.RequestCount())
	}

	// 合法新配置:provider 指向 up2 → SIGHUP → 新请求走 up2(轮询等待热替换落地)
	writeCfg(up2.URL(), "SIGHUP_TEST_KEY_2")
	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	swapped := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_, _ = post()
		if up2.RequestCount() > 0 {
			swapped = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !swapped {
		t.Fatal("SIGHUP 后新请求应走新 provider(up2)")
	}

	// 非法新配置:保持旧态继续服务(绝不半更新)
	writeCfgBroken := func() {
		body := fmt.Sprintf(`server:
  addr: "127.0.0.1:0"
storage:
  sqlite:
    path: %q
providers:
  - name: p1
    adapter: nosuch-adapter
    weight: 1
    base_url: %q
    keys:
      - env: SIGHUP_TEST_KEY_2
model_routes:
  alias-a:
    - { provider: p1, model: up-a, priority: 0 }
`, dbPath, up2.URL())
		if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeCfgBroken()
	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond) // 给(不会发生的)替换留窗口
	before := up2.RequestCount()
	if code, body := post(); code != 200 {
		t.Fatalf("非法配置应保持旧态继续服务: %d %s", code, body)
	}
	if up2.RequestCount() <= before {
		t.Fatal("旧态应仍经 up2 服务(拓扑未被拆毁)")
	}
}
