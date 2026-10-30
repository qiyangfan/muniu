// 验证 OpenAPI 中声明的路由与装配时注册的路由一致。
// openapi.yaml 为手写维护，改路由必须同步改 yaml 或反之，本测试防两者漂移。
package main

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"

	"github.com/solvela/muniu/internal/config"
	"github.com/solvela/muniu/internal/testkit"
)

// TestRoutes_与OpenAPI契约无漂移 双向比对 openapi.yaml 契约集合与装配注册集合。
func TestRoutes_与OpenAPI契约无漂移(t *testing.T) {
	// 1) 解析 openapi.yaml 为方法×路径集合，/console/* 静态资源不在契约内。
	raw, err := os.ReadFile(filepath.Join("..", "..", "contracts", "openapi.yaml"))
	if err != nil {
		t.Fatalf("读取 openapi.yaml: %v", err)
	}
	var doc struct {
		Paths map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("openapi.yaml 非法: %v", err)
	}
	contract := map[string]bool{}
	for path, methods := range doc.Paths {
		for m := range methods {
			switch strings.ToLower(m) {
			case "get", "post", "put", "patch", "delete":
				contract[strings.ToUpper(m)+" "+path] = true
			}
		}
	}
	if len(contract) < 10 {
		t.Fatalf("契约路径过少,疑似解析失败: %d", len(contract))
	}

	// 2) 装配真实路由表，管理面开启。
	t.Setenv("MUNIU_ADMIN_TOKEN", "test-admin-token")
	t.Setenv("E2E_FAKE_KEY", "sk-e2e")
	up := testkit.StartJSONUpstream(t, 200, testkit.JSONOKBody)
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "muniu.yaml")
	cfgContent := `
health:
  enabled: false
providers:
  - name: openai
    adapter: openai
    base_url: "` + up.URL() + `"
    keys: [ { env: E2E_FAKE_KEY } ]
    weight: 1
model_routes:
  alias-a:
    - { provider: openai, model: up-a, priority: 0 }
`
	if err := os.WriteFile(cfgPath, []byte(cfgContent), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	_, routes, cleanup, err := assemble(cfg, "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("装配失败: %v", err)
	}
	cleanup()

	// 3) 集合比对：双向差异都必须为空。
	actual := map[string]bool{}
	for _, r := range routes {
		actual[r] = true
	}
	var missing, extra []string
	for k := range contract {
		if !actual[k] {
			missing = append(missing, k)
		}
	}
	for k := range actual {
		if !contract[k] {
			extra = append(extra, k)
		}
	}
	if len(missing) > 0 || len(extra) > 0 {
		t.Fatalf("路由漂移:\n  契约有而未注册: %v\n  注册了而契约没有: %v\n(修复:同步 contracts/openapi.yaml 与装配代码)",
			formatSorted(missing), formatSorted(extra))
	}
}

func formatSorted(list []string) string {
	out := make([]string, 0, len(list))
	for _, v := range list {
		out = append(out, fmt.Sprintf("%q", v))
	}
	return "[" + strings.Join(out, ", ") + "]"
}
