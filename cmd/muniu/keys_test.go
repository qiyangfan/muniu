// keys 子命令测试:create→list→revoke 全链(临时库),含明文一次性展示与吊销幂等语义。
package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/solvela/muniu/internal/config"
)

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	p := filepath.Join(t.TempDir(), "muniu.yaml")
	content := `
storage:
  sqlite:
    path: "` + filepath.Join(t.TempDir(), "kv") + `"
providers:
  - name: a
    adapter: openai
    base_url: "https://api.a.com/v1"
    keys:
      - env: K1
    weight: 1
model_routes:
  alias-a:
    - { provider: a, model: up-a, priority: 0 }
`
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestKeysCLI_create_list_revoke全链(t *testing.T) {
	cfg := testConfig(t)

	var out, errOut bytes.Buffer
	// create:缺 name 报错
	if code := keysMain(cfg, []string{"create"}, &out, &errOut); code != 2 {
		t.Fatalf("缺 --name 应退出码 2,实际 %d", code)
	}
	out.Reset()
	if code := keysMain(cfg, []string{"create", "--name", "kongming", "--rpm", "120"}, &out, &errOut); code != 0 {
		t.Fatalf("create 失败: %d %s", code, errOut.String())
	}
	plaintext := lineOf(out.String(), "key:")
	if !strings.HasPrefix(plaintext, "mk-") {
		t.Fatalf("明文形态不符: %q", plaintext)
	}
	prefix := lineOf(out.String(), "prefix:")
	if !strings.Contains(errOut.String(), "仅此一次") {
		t.Fatalf("create 必须在 stderr 警示明文仅此一次: %s", errOut.String())
	}

	// list:能看到刚建的 key
	out.Reset()
	if code := keysMain(cfg, []string{"list"}, &out, &errOut); code != 0 {
		t.Fatalf("list 失败: %d", code)
	}
	if !strings.Contains(out.String(), prefix) || !strings.Contains(out.String(), "kongming") {
		t.Fatalf("list 未含新 key: %s", out.String())
	}

	// revoke:成功后再 revoke 同 prefix 返回 1(无 active 行)
	out.Reset()
	if code := keysMain(cfg, []string{"revoke", prefix}, &out, &errOut); code != 0 {
		t.Fatalf("revoke 失败: %d %s", code, errOut.String())
	}
	if code := keysMain(cfg, []string{"revoke", prefix}, &out, &errOut); code != 1 {
		t.Fatalf("重复 revoke 应退出码 1(无 active 行),实际 %d", code)
	}
}

func TestKeysCLI_未知子命令(t *testing.T) {
	cfg := testConfig(t)
	var out, errOut bytes.Buffer
	if code := keysMain(cfg, []string{"frobnicate"}, &out, &errOut); code != 2 {
		t.Fatalf("未知子命令应退出码 2,实际 %d", code)
	}
	if !strings.Contains(errOut.String(), "用法") {
		t.Fatalf("应输出用法: %s", errOut.String())
	}
}

// lineOf 从多行 key: value 输出中取指定字段的值。
func lineOf(s, field string) string {
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(line, field) {
			return strings.TrimSpace(strings.TrimPrefix(line, field))
		}
	}
	return ""
}
