// config 测试：定位顺序、${VAR} 展开 fail-fast 一次报全、结构校验一次报全。
// muniu.example.yaml 是合法样例 fixture，必须永远通过。
package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/solvela/muniu/internal/adapter"
)

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "muniu.yaml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// minimalYAML 最小合法配置，仅 log.level 可变，定位顺序测试用。
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
	// 一次性报全：以下每条问题都必须出现在同一次错误里
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
metering:
  fx_usd_cny_milli: 7200
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

// loadOK 加载合法配置的测试助手，失败即 Fatal。
func loadOK(t *testing.T, y string) *Config {
	t.Helper()
	cfg, err := Load(writeTemp(t, y))
	if err != nil {
		t.Fatalf("配置应合法: %v", err)
	}
	return cfg
}

// TestLoad_拓扑数值域守门：weight/priority 超出数据面 int 域(MaxTopologyInt)必须在
// YAML 入口拒绝，超域巨值进 failover 加权累加会溢出为负，加权随机失效。
func TestLoad_拓扑数值域守门(t *testing.T) {
	p := writeTemp(t, `
providers:
  - name: a
    adapter: openai
    base_url: "https://api.a.com/v1"
    keys:
      - env: K1
        weight: 2147483648
    weight: 2147483649
    priority: 0
model_routes:
  m1:
    - { provider: a, model: m, priority: 0, weight: 2147483648 }
`)
	_, err := Load(p)
	if err == nil {
		t.Fatal("超 int 域的 weight/priority 必须被拒")
	}
	for _, want := range []string{
		"keys[0] weight 超出数值域",
		"weight 必须 >0 且 ≤2147483647",
		"candidates[0] weight 超出数值域",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("错误信息缺 %q: %v", want, err)
		}
	}
}

// TestLoad_适配器值域单源：合法取值必须来自 adapter.Types()，config 不得手抄适配器清单。
// 无实现的形态如 cohere，文档中出现但 For 无构造分支，须在加载期即拒而非拖到装配期。
func TestLoad_适配器值域单源(t *testing.T) {
	wantDomain := strings.Join(adapter.Types(), "|")

	p := writeTemp(t, `
providers:
  - name: g
    adapter: cohere
    base_url: "https://api.cohere.com/compatibility/v1"
    keys:
      - env: COHERE_API_KEY
    weight: 10
`)
	_, err := Load(p)
	if err == nil {
		t.Fatal(`adapter: cohere 无 For 实现,必须在配置加载期被拒`)
	}
	msg := err.Error()
	if !strings.Contains(msg, "providers[0](g)") {
		t.Fatalf("错误必须定位到具体条目: %v", err)
	}
	if !strings.Contains(msg, `允许 `+wantDomain) {
		t.Fatalf("错误提示列举的值域必须与 adapter.Types() 一致(应为 %q): %v", wantDomain, msg)
	}
}

// TestLoad_native_responses声明：openai 系声明生效；anthropic 适配器声明即配置错误，
// 其直通由适配器类型本身声明。
func TestLoad_native_responses声明(t *testing.T) {
	p := writeTemp(t, `
providers:
  - name: a
    adapter: openai
    weight: 1
    native_responses: true
    base_url: "https://api.a.com/v1"
    keys:
      - env: K1
model_routes:
  alias-a:
    - { provider: a, model: upstream-a, priority: 0 }
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("openai 系声明应通过: %v", err)
	}
	if !cfg.Providers[0].NativeResponses {
		t.Fatal("声明应入 schema")
	}

	p2 := writeTemp(t, `
providers:
  - name: b
    adapter: anthropic
    weight: 1
    native_responses: true
    base_url: "https://api.b.com/v1"
    keys:
      - env: K2
model_routes:
  alias-b:
    - { provider: b, model: upstream-b, priority: 0 }
`)
	_, err = Load(p2)
	if err == nil || !strings.Contains(err.Error(), "native_responses") {
		t.Fatalf("anthropic 声明应拒绝: %v", err)
	}

	// gemini 桥接翻译型无原生直通 lane，同样拒绝：静默 no-op 会伪装成已生效
	p3 := writeTemp(t, `
providers:
  - name: g
    adapter: gemini
    weight: 1
    native_responses: true
    base_url: "https://generativelanguage.googleapis.com/v1beta"
    keys:
      - env: K3
model_routes:
  alias-g:
    - { provider: g, model: gemini-x, priority: 0 }
`)
	_, err = Load(p3)
	if err == nil || !strings.Contains(err.Error(), "native_responses") {
		t.Fatalf("gemini 声明应拒绝: %v", err)
	}
}

func TestLoad_nativeMultimodalEmbeddingURL声明(t *testing.T) {
	valid := writeTemp(t, `
providers:
  - name: dashscope
    adapter: qwen
    base_url: "https://dashscope.aliyuncs.com/compatible-mode/v1"
    native_multimodal_embeddings_url: "https://dashscope.aliyuncs.com/api/v1/services/embeddings/multimodal-embedding/multimodal-embedding"
    keys:
      - env: DASHSCOPE_API_KEY
    weight: 1
model_routes:
  qwen3-vl-embedding:
    - { provider: dashscope, model: qwen3-vl-embedding, priority: 0 }
`)
	cfg, err := Load(valid)
	if err != nil {
		t.Fatalf("Qwen 原生 URL 应通过: %v", err)
	}
	if got := cfg.Providers[0].NativeMultimodalEmbeddingsURL; got != "https://dashscope.aliyuncs.com/api/v1/services/embeddings/multimodal-embedding/multimodal-embedding" {
		t.Fatalf("原生 URL 未解析: %q", got)
	}

	for name, provider := range map[string]string{
		"unsupported adapter": `adapter: openai\n    native_multimodal_embeddings_url: "https://api.example.com/native"`,
		"invalid scheme":      `adapter: qwen\n    native_multimodal_embeddings_url: "ftp://api.example.com/native"`,
		"relative URL":        `adapter: qwen\n    native_multimodal_embeddings_url: "/native"`,
	} {
		t.Run(name, func(t *testing.T) {
			yaml := strings.Replace(provider, `\n`, "\n    ", 1)
			path := writeTemp(t, `
providers:
  - name: dashscope
    `+yaml+`
    base_url: "https://dashscope.aliyuncs.com/compatible-mode/v1"
    keys:
      - env: DASHSCOPE_API_KEY
    weight: 1
model_routes:
  alias:
    - { provider: dashscope, model: qwen3-vl-embedding, priority: 0 }
`)
			if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "native_multimodal_embeddings_url") {
				t.Fatalf("错误声明应在配置加载期被拒: %v", err)
			}
		})
	}
}

func TestLoad_pricingVisionInput(t *testing.T) {
	path := writeTemp(t, minimalYAML("error")+`
pricing:
  alias-a:
    currency: CNY
    input: "1.00"
    vision_input: "2.50"
    output: "3.00"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Pricing["alias-a"].VisionInput; got != "2.50" {
		t.Fatalf("YAML vision_input 未解析: %q", got)
	}
}

// TestLoad_alerts配置面：URL 形态校验与窗口非负。
func TestLoad_alerts配置面(t *testing.T) {
	write := func(alerts string) string {
		return writeTemp(t, `
providers:
  - name: a
    adapter: openai
    weight: 1
    base_url: "https://api.a.com/v1"
    keys:
      - env: K1
model_routes:
  alias-a:
    - { provider: a, model: up-a, priority: 0 }
`+alerts)
	}
	cfg, err := Load(write(`
alerts:
  webhook_url: "https://hooks.example.com/muniu"
  dedup_window_sec: 60
`))
	if err != nil || cfg.Alerts.WebhookURL != "https://hooks.example.com/muniu" || cfg.Alerts.DedupWindowSec != 60 {
		t.Fatalf("合法 alerts 应通过: %v %+v", err, cfg.Alerts)
	}
	if _, err := Load(write(`
alerts:
  webhook_url: "ftp://nope"
`)); err == nil || !strings.Contains(err.Error(), "webhook_url") {
		t.Fatalf("非 http(s) 应拒绝: %v", err)
	}
	if _, err := Load(write(`
alerts:
  webhook_url: "https://ok.example.com/x"
  dedup_window_sec: -1
`)); err == nil || !strings.Contains(err.Error(), "dedup_window_sec") {
		t.Fatalf("负窗口应拒绝: %v", err)
	}
}

func TestBalance_缺省值与manual合法声明(t *testing.T) {
	// 手工拼装:balance 顶层节 + 渠道 manual 声明
	full := `
balance: {}
providers:
  - name: a
    adapter: openai
    base_url: "https://api.a.com/v1"
    keys:
      - env: K1
    weight: 10
    balance:
      source: manual
      currency: CNY
      below: "10.00"
model_routes:
  alias-a:
    - { provider: a, model: upstream-a, priority: 0 }
`
	cfg, err := Load(writeTemp(t, full))
	if err != nil {
		t.Fatalf("合法 balance 配置应通过: %v", err)
	}
	if cfg.Balance.Enabled == nil || !*cfg.Balance.Enabled {
		t.Fatalf("enabled 缺省应为 true")
	}
	if cfg.Balance.EvalIntervalSec != 3600 || cfg.Balance.PollTimeoutSec != 10 ||
		cfg.Balance.BurnWindowDays != 7 || cfg.Balance.BurnMinDays != 2 ||
		cfg.Balance.FailureThreshold != 3 || cfg.Balance.RearmRatioMilli != 1200 {
		t.Fatalf("全局缺省值失真: %+v", cfg.Balance)
	}
	if cfg.Balance.BurnSpikeRatioMilli == nil || *cfg.Balance.BurnSpikeRatioMilli != 5000 ||
		cfg.Balance.DropRatioMilli == nil || *cfg.Balance.DropRatioMilli != 500 {
		t.Fatalf("异常消耗旋钮缺省失真: %+v", cfg.Balance)
	}
	if len(cfg.Balance.TierHours) != 5 || cfg.Balance.TierHours[0] != 720 || cfg.Balance.TierHours[4] != 24 {
		t.Fatalf("档位缺省失真: %v", cfg.Balance.TierHours)
	}
	if cfg.Providers[0].Balance == nil || cfg.Providers[0].Balance.Source != "manual" {
		t.Fatalf("渠道级声明失真: %+v", cfg.Providers[0].Balance)
	}
}

func TestBalance_校验错误一次报全(t *testing.T) {
	full := `
balance:
  rearm_ratio_milli: 900
  tier_hours: [24, 720]
providers:
  - name: a
    adapter: openai
    base_url: "https://api.a.com/v1"
    keys:
      - env: K1
    weight: 10
    balance:
      source: deepseek
      below: "abc"
  - name: b
    adapter: openai
    base_url: "https://api.b.com/v1"
    keys:
      - env: K2
    weight: 10
    balance:
      source: gemini
model_routes:
  alias-a:
    - { provider: a, model: upstream-a, priority: 0 }
`
	_, err := Load(writeTemp(t, full))
	if err == nil {
		t.Fatalf("非法配置应拒绝")
	}
	for _, frag := range []string{"rearm_ratio_milli", "tier_hours", "balance.source 非法", "below 非法", "providers[1](b)"} {
		if !strings.Contains(err.Error(), frag) {
			t.Fatalf("错误须一次性报全 %q: %v", frag, err)
		}
	}
}

func TestBalance_渠道级来源字段约束(t *testing.T) {
	cases := []struct {
		name string
		bal  string
		want string
	}{
		{"none带字段", "balance:\n      source: none\n      below: \"1.00\"", "其余字段不得声明"},
		{"bearer带币种", "balance:\n      source: deepseek\n      currency: CNY", "currency 仅 manual/billing-compat 源声明"},
		{"billing-compat币种合法", "balance:\n      source: billing-compat\n      currency: CNY", ""},
		{"billing-compat币种非法", "balance:\n      source: billing-compat\n      currency: EUR", "currency 非法"},
		{"manual缺币种", "balance:\n      source: manual", "currency 非法"},
		{"manual带凭证", "balance:\n      source: manual\n      currency: CNY\n      access_key_env: AK", "凭证字段仅云账务源"},
		{"cloud缺凭证", "balance:\n      source: aliyun-bss", "access_key_env 必填"},
		{"cloud凭证名非法", "balance:\n      source: volc-bss\n      access_key_env: 9AK\n      secret_key_env: SK", "须为环境变量名"},
	}
	// 异常消耗旋钮:显式 0=禁用是合法形态,负值拒绝
	base := `
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
	if _, lerr := Load(writeTemp(t, "balance:\n  burn_spike_ratio_milli: 0\n  drop_ratio_milli: 0\n"+base)); lerr != nil {
		t.Fatalf("旋钮显式 0=禁用应通过: %v", lerr)
	}
	_, nerr := Load(writeTemp(t, "balance:\n  burn_spike_ratio_milli: -1\n  drop_ratio_milli: -500\n"+base))
	if nerr == nil || !strings.Contains(nerr.Error(), "burn_spike_ratio_milli") || !strings.Contains(nerr.Error(), "drop_ratio_milli") {
		t.Fatalf("负旋钮应一次报全两处: %v", nerr)
	}
	for _, c := range cases {
		full := `
providers:
  - name: a
    adapter: openai
    base_url: "https://api.a.com/v1"
    keys:
      - env: K1
    weight: 10
    ` + c.bal + `
model_routes:
  alias-a:
    - { provider: a, model: upstream-a, priority: 0 }
`
		_, err := Load(writeTemp(t, full))
		if c.want == "" { // 空期望=合法声明须通过校验
			if err != nil {
				t.Fatalf("%s: 应通过校验,实际 %v", c.name, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s: 应报 %q,实际 %v", c.name, c.want, err)
		}
	}
}

func TestBalance_source_none缺省零声明(t *testing.T) {
	full := `
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
	cfg, err := Load(writeTemp(t, full))
	if err != nil {
		t.Fatalf("无 balance 声明应通过: %v", err)
	}
	if cfg.Providers[0].Balance != nil {
		t.Fatalf("未声明渠道 Balance 应为 nil")
	}
}

func TestHealth_缺省与校验(t *testing.T) {
	base := `
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
	cfg, err := Load(writeTemp(t, base))
	if err != nil {
		t.Fatalf("无 health 节应通过: %v", err)
	}
	if cfg.Health.Enabled == nil || !*cfg.Health.Enabled || cfg.Health.FreeProbeEnabled == nil || !*cfg.Health.FreeProbeEnabled {
		t.Fatalf("health 开关缺省应全 true: %+v", cfg.Health)
	}
	if cfg.Health.ProbeIntervalSec != 300 || cfg.Health.ProbeTimeoutSec != 10 ||
		cfg.Health.FailureThreshold != 2 || cfg.Health.OpenSeconds != 300 ||
		cfg.Health.SwitchWindowSec != 300 || cfg.Health.SwitchRateMilli != 300 ||
		cfg.Health.SwitchMinSamples != 20 {
		t.Fatalf("health 缺省值失真: %+v", cfg.Health)
	}

	// 渠道级声明:free_probe 退订 + probe_model 声明制合法
	withCh := `
providers:
  - name: a
    adapter: openai
    base_url: "https://api.a.com/v1"
    keys:
      - env: K1
    weight: 10
    health:
      free_probe: false
      probe_model: up-a
model_routes:
  alias-a:
    - { provider: a, model: upstream-a, priority: 0 }
`
	if _, err := Load(writeTemp(t, withCh)); err != nil {
		t.Fatalf("渠道级探针声明应通过: %v", err)
	}

	// 无 key 渠道声明推理探针:校验期拒绝
	noKey := `
providers:
  - name: a
    adapter: openai
    base_url: "https://api.a.com/v1"
    weight: 10
    health:
      probe_model: up-a
model_routes:
  alias-a:
    - { provider: a, model: upstream-a, priority: 0 }
`
	_, err = Load(writeTemp(t, noKey))
	if err == nil || !strings.Contains(err.Error(), "keys 为空") {
		t.Fatalf("无 key 声明推理探针应拒绝: %v", err)
	}

	// 负值拒绝(一次报全)
	_, err = Load(writeTemp(t, "health:\n  probe_interval_sec: -1\n  switch_rate_milli: -5\n"+base))
	if err == nil || !strings.Contains(err.Error(), "probe_interval_sec") || !strings.Contains(err.Error(), "switch_rate_milli") {
		t.Fatalf("负值应一次报全: %v", err)
	}

	// 总开关关闭:合法
	_, err = Load(writeTemp(t, "health:\n  enabled: false\n"+base))
	if err != nil {
		t.Fatalf("总开关关闭应通过: %v", err)
	}
}
