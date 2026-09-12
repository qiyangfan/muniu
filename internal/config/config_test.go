// config 契约测试:定位顺序、${VAR} 展开(fail-fast 一次报全)、结构校验一次报全。
// 校验属测试先行域;`muniu.example.yaml` 是合法样例 fixture,必须永远通过。
package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeTemp 辅助:写临时配置文件并返回路径。
func writeTemp(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "muniu.yaml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// minimalYAML 最小合法配置(仅改 log.level),定位顺序测试用。
func minimalYAML(level string) string {
	return `
log:
  level: ` + level + `
providers:
  - name: a
    adapter: openai
    base_url: "https://api.a.com/v1"
    keys:
      - env: K1
    weight: 10
model_routes:
  alias-a:
    - { provider: a, model: upstream-a, priority: 0 }
`
}

func TestLoad_合法样例fixture(t *testing.T) {
	cfg, err := Load("../../muniu.example.yaml")
	if err != nil {
		t.Fatalf("muniu.example.yaml 必须通过校验,实际报错: %v", err)
	}
	if len(cfg.Providers) != 2 {
		t.Fatalf("providers 数量不符: %d", len(cfg.Providers))
	}
	if cfg.Server.Addr != ":8787" {
		t.Fatalf("addr 解析不符: %q", cfg.Server.Addr)
	}
	if cfg.Server.Timeouts.Dial != 5 || cfg.Server.Timeouts.FirstToken != 60 || cfg.Server.Timeouts.ChunkIdle != 30 {
		t.Fatalf("超时三层解析不符: %+v", cfg.Server.Timeouts)
	}
	if d := cfg.Server.Timeouts.DialDuration(); d != 5*time.Second {
		t.Fatalf("DialDuration 换算不符: %v", d)
	}
	ds, ok := cfg.ProviderByName("deepseek")
	if !ok || ds.BaseURL != "https://api.deepseek.com/v1" || len(ds.Keys) != 1 {
		t.Fatalf("deepseek provider 解析不符: %+v %+v", ds, ok)
	}
	if cands := cfg.ModelRoutes["deepseek-chat"]; len(cands) != 1 || cands[0].Provider != "deepseek" || cands[0].Model != "deepseek-v4-flash" {
		t.Fatalf("model_routes 解析不符: %+v", cands)
	}
	if cfg.Pricing["gpt-4o-mini"].Currency != "USD" {
		t.Fatalf("pricing 解析不符: %+v", cfg.Pricing["gpt-4o-mini"])
	}
	if cfg.Log.Level != "info" || cfg.Log.Format != "json" {
		t.Fatalf("log 解析不符: %+v", cfg.Log)
	}
}

func TestLoad_定位优先级_flag高于环境变量高于默认(t *testing.T) {
	envFile := writeTemp(t, minimalYAML("warn"))
	flagFile := writeTemp(t, minimalYAML("debug"))
	dir := t.TempDir()

	t.Setenv("MUNIU_CONFIG", envFile)
	// flag 优先于 env
	cfg, err := Load(flagFile)
	if err != nil || cfg.Log.Level != "debug" {
		t.Fatalf("flag 定位失效: %v %+v", err, cfg)
	}
	// 无 flag 时回落 env
	cfg, err = Load("")
	if err != nil || cfg.Log.Level != "warn" {
		t.Fatalf("MUNIU_CONFIG 定位失效: %v %+v", err, cfg)
	}
	// 无 flag 无 env 时回落 ./muniu.yaml
	t.Setenv("MUNIU_CONFIG", "")
	if err := os.WriteFile(filepath.Join(dir, "muniu.yaml"), []byte(minimalYAML("error")), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	cfg, err = Load("")
	if err != nil || cfg.Log.Level != "error" {
		t.Fatalf("./muniu.yaml 默认定位失效: %v %+v", err, cfg)
	}
}

func TestLoad_显式指定但文件不存在必须报错(t *testing.T) {
	if _, err := Load("/nonexistent/muniu.yaml"); err == nil {
		t.Fatal("显式指定不存在的配置必须报错(fail-fast)")
	}
}

func TestLoad_缺环境变量一次报全(t *testing.T) {
	p := writeTemp(t, `
providers:
  - name: a
    adapter: openai
    base_url: "https://api.a.com/v1"
    keys:
      - env: ${MUNIU_TEST_MISSING_A}
      - env: ${MUNIU_TEST_MISSING_B}
    weight: 10
`)
	_, err := Load(p)
	if err == nil {
		t.Fatal("缺环境变量必须报错")
	}
	for _, name := range []string{"MUNIU_TEST_MISSING_A", "MUNIU_TEST_MISSING_B"} {
		if !strings.Contains(err.Error(), name) {
			t.Fatalf("错误未列全缺失变量 %s: %v", name, err)
		}
	}
}

func TestLoad_环境变量展开生效(t *testing.T) {
	t.Setenv("MUNIU_TEST_SET_OK", "hello")
	p := writeTemp(t, `
storage:
  sqlite:
    path: "data/${MUNIU_TEST_SET_OK}.db"
providers:
  - name: a
    adapter: openai
    base_url: "https://api.a.com/v1"
    keys:
      - env: MUNIU_TEST_SET_OK
    weight: 10
model_routes:
  alias-a:
    - { provider: a, model: upstream-a, priority: 0 }
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("展开后应通过: %v", err)
	}
	if cfg.Storage.SQLite.Path != "data/hello.db" {
		t.Fatalf("展开结果不符: %q", cfg.Storage.SQLite.Path)
	}
}

func TestLoad_校验错误一次报全(t *testing.T) {
	p := writeTemp(t, `
providers:
  - name: a
    adapter: unknown-adapter
    base_url: "not-a-url"
    keys:
      - env: K1
    weight: 0
    priority: -1
  - name: a
    adapter: openai
    base_url: "https://api.b.com/v1"
    keys: []
    weight: 10
model_routes:
  ghost-route:
    - { provider: no-such-provider, model: m, priority: 0 }
  empty-route: []
pricing:
  x:
    currency: EUR
    input: "1.00"
    output: "2.00"
`)
	_, err := Load(p)
	if err == nil {
		t.Fatal("多项问题必须报错")
	}
	// 一次性报全:以下每条问题都必须出现在同一次错误里
	for _, want := range []string{
		"not-a-url",        // 坏 URL
		"unknown-adapter",  // 适配器枚举
		"weight",           // weight 必须 >0
		"priority",         // priority 必须 ≥0
		"no-such-provider", // 悬空路由
		"empty-route",      // 空候选列表
		"keys",             // 空 key 列表
		"重名",               // provider 重名
		"EUR",              // 币种枚举
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("错误未一次性报全(缺 %q): %v", want, err)
		}
	}
}

func TestLoad_合法最小配置通过(t *testing.T) {
	p := writeTemp(t, `
providers:
  - name: a
    adapter: openai
    base_url: "https://api.a.com/v1"
    keys:
      - env: K1
    weight: 1
model_routes:
  alias-a:
    - { provider: a, model: upstream-a, priority: 0 }
`)
	if _, err := Load(p); err != nil {
		t.Fatalf("最小合法配置应通过: %v", err)
	}
}

func TestLoad_分时段倍率窗合法(t *testing.T) {
	cfg, err := Load(writeTemp(t, `
pricing:
  deepseek-chat:
    currency: USD
    input: 0.44
    output: 1.32
    timezone: "+08:00"
    time_windows:
      - { window: "00:30-08:30", multiplier: "0.5" }
providers:
  - { name: a, adapter: openai, base_url: "https://api.a.com/v1", keys: [{ env: K1 }], weight: 10 }
model_routes:
  alias-a:
    - { provider: a, model: upstream-a, priority: 0 }
`))
	if err != nil {
		t.Fatalf("合法分时段配置应通过: %v", err)
	}
	pr := cfg.Pricing["deepseek-chat"]
	if pr.Timezone != "+08:00" || len(pr.TimeWindows) != 1 ||
		pr.TimeWindows[0].Window != "00:30-08:30" || pr.TimeWindows[0].Multiplier != "0.5" {
		t.Fatalf("分时段配置解析不符: %+v", pr)
	}
}

func TestLoad_分时段倍率窗非法一次列全(t *testing.T) {
	_, err := Load(writeTemp(t, `
pricing:
  a:
    currency: USD
    input: "0.44"
    output: "1.32"
    timezone: "Asia/Shanghai"
    time_windows:
      - { window: "25:00-26:00", multiplier: "0.5" }
  b:
    currency: USD
    input: "0.44"
    output: "1.32"
    time_windows:
      - { window: "00:30-08:30", multiplier: "0" }
      - { window: "08:00-08:00", multiplier: "1.5" }
providers:
  - { name: x, adapter: openai, base_url: "https://api.x.com/v1", keys: [{ env: K1 }], weight: 10 }
model_routes:
  alias-x:
    - { provider: x, model: upstream-x, priority: 0 }
`))
	if err == nil {
		t.Fatal("非法分时段配置必须拒绝")
	}
	for _, want := range []string{"pricing[a]", "非法时区", "pricing[b].time_windows[0].multiplier", "pricing[b].time_windows[1]"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("校验错误应一次列全(缺 %q): %v", want, err)
		}
	}
}

func TestLoad_插件配置缺省与解析(t *testing.T) {
	cfg := loadOK(t, `
plugins:
  - name: pii-masker
    path: ./plugins/pii_masker.wasm
    config:
      rules: phone
  - name: guard
    path: ./plugins/guard.wasm
    enabled: false
    fail_open: true
    timeout_ms: 200
    memory_limit_mib: 32
    pool_size: 4
providers:
  - { name: x, adapter: openai, base_url: "https://api.x.com/v1", keys: [{ env: K1 }], weight: 10 }
model_routes:
  alias-x:
    - { provider: x, model: upstream-x, priority: 0 }
`)
	if len(cfg.Plugins) != 2 {
		t.Fatalf("应解析 2 个插件,实际 %d", len(cfg.Plugins))
	}
	p0 := cfg.Plugins[0]
	if p0.Enabled == nil || *p0.Enabled != true {
		t.Fatalf("enabled 缺省应为 true: %+v", p0.Enabled)
	}
	if p0.TimeoutMs != 0 || p0.MemoryLimitMiB != 0 || p0.PoolSize != 0 {
		t.Fatalf("未配置的资源项应保持零值(由运行时取缺省): %+v", p0)
	}
	if p0.Config["rules"] != "phone" {
		t.Fatalf("config 透传不符: %+v", p0.Config)
	}
	p1 := cfg.Plugins[1]
	if *p1.Enabled != false || !p1.FailOpen || p1.TimeoutMs != 200 || p1.MemoryLimitMiB != 32 || p1.PoolSize != 4 {
		t.Fatalf("显式配置项解析不符: %+v", p1)
	}
}

func TestLoad_插件配置非法一次列全(t *testing.T) {
	_, err := Load(writeTemp(t, `
plugins:
  - { path: ./a.wasm }
  - { name: dup, path: ./a.wasm }
  - { name: dup, path: ./b.wasm }
  - { name: neg, path: ./c.wasm, timeout_ms: -1, memory_limit_mib: 999, pool_size: 99 }
  - { name: huge, path: ./d.wasm, timeout_ms: 20000 }
providers:
  - { name: x, adapter: openai, base_url: "https://api.x.com/v1", keys: [{ env: K1 }], weight: 10 }
model_routes:
  alias-x:
    - { provider: x, model: upstream-x, priority: 0 }
`))
	if err == nil {
		t.Fatal("非法插件配置必须拒绝")
	}
	for _, want := range []string{
		"plugins[0] 缺 name", "plugins[2] name \"dup\" 重名", "plugins[3](neg) timeout_ms 不可为负",
		"plugins[3](neg) memory_limit_mib", "plugins[3](neg) pool_size", "plugins[4](huge) timeout_ms(20000) 超过硬顶",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("校验错误应一次列全(缺 %q): %v", want, err)
		}
	}
}

// loadOK 加载合法配置的测试助手(失败即 Fatal)。
func loadOK(t *testing.T, y string) *Config {
	t.Helper()
	cfg, err := Load(writeTemp(t, y))
	if err != nil {
		t.Fatalf("配置应合法: %v", err)
	}
	return cfg
}
