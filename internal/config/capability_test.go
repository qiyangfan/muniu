// 模型能力目录(ADR-0010)配置契约测试:合法解析、非法声明加载期一次列全。
// 测试先行域:先红后绿。
package config

import (
	"strings"
	"testing"
)

// capabilityYAML 合法样例(基于 minimalYAML 同款 provider)。
const capabilityYAML = `
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
models:
  - provider: a
    model: upstream-a
    drop_params: [temperature, top_p]
    thinking_lock: true
    max_output_tokens: 8192
strict_governance: true
`

func TestLoad_模型能力声明合法解析(t *testing.T) {
	cfg, err := Load(writeTemp(t, capabilityYAML))
	if err != nil {
		t.Fatalf("合法能力声明必须通过: %v", err)
	}
	if len(cfg.Models) != 1 {
		t.Fatalf("models 解析数量不符: %d", len(cfg.Models))
	}
	mc := cfg.Models[0]
	if mc.Provider != "a" || mc.Model != "upstream-a" {
		t.Fatalf("(provider, model) 解析不符: %+v", mc)
	}
	if len(mc.DropParams) != 2 || mc.DropParams[0] != "temperature" || mc.DropParams[1] != "top_p" {
		t.Fatalf("drop_params 解析不符: %+v", mc.DropParams)
	}
	if !mc.ThinkingLock || mc.MaxOutputTokens != 8192 {
		t.Fatalf("thinking_lock/max_output_tokens 解析不符: %+v", mc)
	}
	if !cfg.StrictGovernance {
		t.Fatal("strict_governance 解析不符")
	}
}

func TestLoad_模型能力声明非法一次列全(t *testing.T) {
	bad := `
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
models:
  - provider: ghost
    model: m1
  - provider: a
    model: m2
    max_output_tokens: -5
    drop_params: ["", temperature]
  - provider: a
    model: m1
  - provider: a
    model: m1
  - provider: a
    drop_params: [temperature]
  - model: m9
  - provider: a
    model: m3
    drop_params: [" temperature ", messages]
strict_governance: true
`
	_, err := Load(writeTemp(t, bad))
	if err == nil {
		t.Fatal("非法能力声明必须拒绝启动")
	}
	for _, want := range []string{
		"ghost",      // 引用不存在的 provider
		"m2",         // 负数上限 / 空 drop 参数(同条目)
		"-5",         // 负数上限
		"重名",         // (a, m1) 重复声明
		"缺 model",    // 条目缺 model
		"缺 provider", // 条目缺 provider
		"含首尾空白",      // " temperature " 声明会静默失效
		"协议结构字段",     // messages 禁止声明治理
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("错误信息缺 %q:\n%s", want, err.Error())
		}
	}
}
