package plugin

// 使用 plugins/build.sh 生成的 WASM 工件验证插件 ABI 和端到端行为。
// 工件不存在时跳过；Go CI 环境不要求安装 Rust 工具链。

import (
	"context"
	"os"
	"strings"
	"testing"
)

const (
	piiMaskerArtifact    = "../../plugins/pii-masker/pii_masker.wasm"
	jsonRedactorArtifact = "../../plugins/json-redactor/json_redactor.wasm"
)

func compileReal(t *testing.T, path, name string, config map[string]string) *Plugin {
	t.Helper()
	bin, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("真插件工件不存在(先跑 plugins/build.sh): %v", err)
	}
	// 首次加载包含 WASM 编译，探测超时按上限设置以覆盖冷启动耗时。
	p, err := Compile(Options{Name: name, Binary: bin, Config: config, ProbeTimeoutMs: 60_000})
	if err != nil {
		t.Fatalf("编译/注册真插件 %s: %v", name, err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p
}

func TestRealArtifact_PiiMasker_改写链路与代际回收(t *testing.T) {
	p := compileReal(t, piiMaskerArtifact, "pii-masker", nil)
	ctx := context.Background()

	// PII 紧贴中文或由空格分隔时，三类内容均应脱敏。
	v := p.HookRequestBody(ctx, []byte(
		`{"model":"qwen-plus","messages":[{"role":"user","content":"证件11010119900307551X邮件a@b.com电话 13912345678"}]}`))
	if v.Kind != VerdictKindModified {
		t.Fatalf("期望改写,实为 verdict=%v reason=%q", v.Kind, v.Reason)
	}
	out := string(v.Body)
	for _, want := range []string{"[ID]", "[EMAIL]", "[PHONE]"} {
		if !strings.Contains(out, want) {
			t.Fatalf("改写产物缺 %s: %s", want, out)
		}
	}
	if strings.Contains(out, "11010119900307551X") || strings.Contains(out, "13912345678") {
		t.Fatalf("PII 残留: %s", out)
	}

	// 无命中放行，同实例池复用且经双区代际重置。
	v = p.HookRequestBody(ctx, []byte(`{"messages":[{"role":"user","content":"你好"}]}`))
	if v.Kind != VerdictKindPass {
		t.Fatalf("无命中期望放行,实为 verdict=%v body=%s", v.Kind, v.Body)
	}

	// 再次改写，第三个代际，验证池化实例跨代际稳定。
	v = p.HookRequestBody(ctx, []byte(`{"input":["联系a@b.com"]}`))
	if v.Kind != VerdictKindModified || !strings.Contains(string(v.Body), "[EMAIL]") {
		t.Fatalf("第三请求期望改写含 [EMAIL],实为 verdict=%v body=%s", v.Kind, v.Body)
	}

	// parts 数组的 text 段与 embeddings 字符串数组同样命中。
	v = p.HookRequestBody(ctx, []byte(
		`{"messages":[{"role":"user","content":[{"type":"text","text":"邮件x@y.cn"}]}]}`))
	if v.Kind != VerdictKindModified || !strings.Contains(string(v.Body), "[EMAIL]") {
		t.Fatalf("parts text 段期望改写,实为 verdict=%v body=%s", v.Kind, v.Body)
	}
}

func TestRealArtifact_PiiMasker_自定义规则与坏配置拒载(t *testing.T) {
	p := compileReal(t, piiMaskerArtifact, "pii-masker-rules", map[string]string{
		"rules": `[{"pattern":"1[3-9]\\d{9}","replacement":"[手机号]"}]`,
	})
	v := p.HookRequestBody(context.Background(),
		[]byte(`{"messages":[{"role":"user","content":"电话13912345678收"}]}`))
	if v.Kind != VerdictKindModified || !strings.Contains(string(v.Body), "[手机号]") {
		t.Fatalf("自定义规则期望改写,实为 verdict=%v body=%s", v.Kind, v.Body)
	}

	// 坏正则导致 init Err，plugin_start 返回 1，注册拒载；坏规则不允许静默降级。
	bin, err := os.ReadFile(piiMaskerArtifact)
	if err != nil {
		t.Skipf("真插件工件不存在: %v", err)
	}
	if _, err := Compile(Options{Name: "pii-masker-bad", Binary: bin,
		Config: map[string]string{"rules": `[{"pattern":"((","replacement":"x"}]`}}); err == nil {
		t.Fatal("坏规则配置必须拒载")
	} else if !strings.Contains(err.Error(), "退出码 1") {
		t.Fatalf("拒载应报退出码 1,实为: %v", err)
	}
}

func TestRealArtifact_JsonRedactor_字段删除(t *testing.T) {
	p := compileReal(t, jsonRedactorArtifact, "json-redactor", map[string]string{
		"rules": `[{"pointer":"/messages/*/name","op":"remove"}]`,
	})
	v := p.HookRequestBody(context.Background(), []byte(
		`{"messages":[{"role":"user","name":"张三","content":"hi"},{"role":"assistant"},"x"]}`))
	if v.Kind != VerdictKindModified {
		t.Fatalf("期望改写,实为 verdict=%v reason=%q", v.Kind, v.Reason)
	}
	out := string(v.Body)
	if strings.Contains(out, `"name"`) || strings.Contains(out, "张三") {
		t.Fatalf("name 字段未删除: %s", out)
	}
	if !strings.Contains(out, `"x"`) || !strings.Contains(out, `"content":"hi"`) {
		t.Fatalf("其余内容必须原样保留: %s", out)
	}

	// 无命中放行。
	v = p.HookRequestBody(context.Background(), []byte(`{"messages":[{"role":"user"}]}`))
	if v.Kind != VerdictKindPass {
		t.Fatalf("无命中期望放行,实为 verdict=%v body=%s", v.Kind, v.Body)
	}
}
