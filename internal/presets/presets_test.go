// 验证预设目录可生成有效配置，且仅包含 adapter.For 支持的适配器。
package presets_test

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/solvela/muniu/internal/adapter"
	"github.com/solvela/muniu/internal/config"
	"github.com/solvela/muniu/internal/presets"
)

// 预设目录和配置校验共用 adapter.Types()，避免适配器清单与工厂实现不一致。
var implementedAdapters = func() map[string]bool {
	m := make(map[string]bool, len(adapter.Types()))
	for _, t := range adapter.Types() {
		m[t] = true
	}
	return m
}()

var validGroups = map[string]bool{
	presets.GroupDomestic: true, presets.GroupGlobal: true, presets.GroupLocal: true,
}

// TestCatalog_字段自洽 目录逐条字段契约：唯一别名、合法 URL、https 文档、大写下划线 env、合法分组。
func TestCatalog_字段自洽(t *testing.T) {
	seenAlias := map[string]bool{}
	for _, p := range presets.Catalog() {
		if p.Alias == "" || seenAlias[p.Alias] {
			t.Fatalf("alias 空或重复: %q", p.Alias)
		}
		seenAlias[p.Alias] = true
		if p.Display == "" {
			t.Fatalf("%s: display 不得为空", p.Alias)
		}
		if !implementedAdapters[p.Adapter] {
			t.Fatalf("%s: adapter %q 不在 adapter.For 已实现集合", p.Alias, p.Adapter)
		}
		u, err := url.Parse(p.BaseURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			t.Fatalf("%s: base_url 非法: %q", p.Alias, p.BaseURL)
		}
		if u.Scheme == "http" && !strings.Contains(u.Host, "127.0.0.1") && !strings.Contains(u.Host, "localhost") {
			t.Fatalf("%s: 明文 http 仅允许本地回环: %q", p.Alias, p.BaseURL)
		}
		du, derr := url.Parse(p.Docs)
		if derr != nil || du.Scheme != "https" || du.Host == "" {
			t.Fatalf("%s: docs 须为 https URL: %q", p.Alias, p.Docs)
		}
		if p.Env == "" || p.Env != strings.ToUpper(p.Env) {
			t.Fatalf("%s: env 须为非空大写: %q", p.Alias, p.Env)
		}
		if !validGroups[p.Group] {
			t.Fatalf("%s: group 非法: %q", p.Alias, p.Group)
		}
		if p.Auth != "" && p.Auth != "bearer" {
			t.Fatalf("%s: auth 值域非法: %q", p.Alias, p.Auth)
		}
		if p.Auth != "" && p.Adapter != "anthropic" {
			t.Fatalf("%s: auth 声明仅 anthropic 适配器可携带", p.Alias)
		}
		seenModel := map[string]bool{}
		for _, m := range p.Models {
			if m == "" || seenModel[m] {
				t.Fatalf("%s: models 空或重复: %q", p.Alias, m)
			}
			seenModel[m] = true
		}
	}
}

// TestCatalog_抄进配置可通过校验 每条预设渲染成 YAML 后必须通过真实 config.Load 全流程，
// 即 yaml tag、展开、校验；目录与配置 schema 漂移如 adapter 改名、base_url 规则收紧在此立即见红。
func TestCatalog_抄进配置可通过校验(t *testing.T) {
	var b strings.Builder
	b.WriteString("server:\n  addr: \":0\"\nstorage:\n  sqlite:\n    path: \":memory:\"\nproviders:\n")
	for _, p := range presets.Catalog() {
		b.WriteString("  - name: " + p.Alias + "\n")
		b.WriteString("    adapter: " + p.Adapter + "\n")
		b.WriteString("    base_url: \"" + p.BaseURL + "\"\n")
		b.WriteString("    keys:\n      - env: " + p.Env + "\n")
		b.WriteString("    weight: 1\n")
	}
	path := filepath.Join(t.TempDir(), "presets.yaml")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatalf("写临时配置: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("目录整体作为 providers 未通过 config.Load: %v", err)
	}
	if len(cfg.Providers) != len(presets.Catalog()) {
		t.Fatalf("载入 provider 数不符: got %d want %d", len(cfg.Providers), len(presets.Catalog()))
	}
}

// TestCatalog_分组完备 三组各有条目，UI 分组渲染不出现空组；本地运行时必须免密钥占位语义。
func TestCatalog_分组完备(t *testing.T) {
	perGroup := map[string]int{}
	for _, p := range presets.Catalog() {
		perGroup[p.Group]++
		if p.Group == presets.GroupLocal && p.Note == "" {
			t.Fatalf("%s: 本地运行时必须带「env 占位」说明", p.Alias)
		}
	}
	for g := range validGroups {
		if perGroup[g] == 0 {
			t.Fatalf("分组 %s 无任何条目", g)
		}
	}
}

// TestCatalog_桥接裸域 验证 Anthropic 预设的 base_url 不以斜杠结尾。
func TestCatalog_桥接裸域(t *testing.T) {
	for _, p := range presets.Catalog() {
		if p.Adapter == "anthropic" && strings.TrimRight(p.BaseURL, "/") != p.BaseURL {
			t.Fatalf("%s: 桥接 base_url 不得带尾斜杠: %q", p.Alias, p.BaseURL)
		}
	}
}

// TestCatalog_鉴权声明口径 官方 Anthropic 保持协议缺省 x-api-key，其文档明确 Bearer 仅供 OAuth，
// 发 API key 形态即 401；国内六家分支按各自官方文档 AUTH_TOKEN 的 Bearer 口径声明 bearer。
func TestCatalog_鉴权声明口径(t *testing.T) {
	for _, p := range presets.Catalog() {
		if p.Adapter != "anthropic" {
			continue
		}
		switch p.Alias {
		case "anthropic":
			if p.Auth != "" {
				t.Fatalf("官方 Anthropic 应保持缺省 x-api-key,实际 %q", p.Auth)
			}
		default:
			if p.Auth != "bearer" {
				t.Fatalf("国内分支 %s 应声明 bearer(官方文档 AUTH_TOKEN 口径),实际 %q", p.Alias, p.Auth)
			}
		}
	}
}
