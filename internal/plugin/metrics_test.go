// per-plugin 指标文本协议测试(任务 5.2):调用计数{verdict}/耗时直方图/containment{kind}
// 四类序列全部进 /metrics 渲染输出;禁用态(全局开关)不产出。
package plugin

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/solvela/muniu/internal/metrics"
	"github.com/solvela/muniu/internal/testkit"
)

func TestPluginMetrics_文本协议(t *testing.T) {
	metrics.SetEnabled(true)
	t.Cleanup(func() { metrics.SetEnabled(false) })

	// 一放行一拒绝,一次 containment(死循环超时)
	pPass := compileFor(t, "m-pass", testkit.FixturePass)
	_ = pPass.HookRequestBody(context.Background(), testkit.FixtureChatBody("m"))
	pRej := compileFor(t, "m-rej", testkit.FixtureReject)
	_ = pRej.HookRequestBody(context.Background(), testkit.FixtureChatBody("m"))
	pLoop, err := Compile(Options{Name: "m-loop", Binary: testkit.FixturePlugin(testkit.FixtureLoop),
		TimeoutMs: 50, MemoryLimitPages: 1, PoolSize: 1,
		Logger: discardLogger()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pLoop.Close() })
	_ = pLoop.HookRequestBody(context.Background(), testkit.FixtureChatBody("m"))

	var b bytes.Buffer
	metrics.RenderTo(&b)
	out := b.String()
	for _, want := range []string{
		"# TYPE muniu_plugin_calls_total counter",
		`muniu_plugin_calls_total{plugin="m-pass",verdict="pass"} 1`,
		`muniu_plugin_calls_total{plugin="m-rej",verdict="rejected"} 1`,
		`muniu_plugin_calls_total{plugin="m-loop",verdict="failed"} 1`,
		"# TYPE muniu_plugin_call_duration_ms histogram",
		`muniu_plugin_call_duration_ms_bucket{le="+Inf"} 3`,
		`muniu_plugin_call_duration_ms_count 3`,
		"# TYPE muniu_plugin_containment_total counter",
		`muniu_plugin_containment_total{plugin="m-loop",kind="timeout"} 1`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("指标输出缺 %q:\n%s", want, out)
		}
	}

	// 实例水位仪表(GaugeFunc 在 NewRegistry 注册;需注册表内有插件才有序列)
	reg := NewRegistry()
	t.Cleanup(reg.Close)
	if _, _, err := reg.Register(Options{Name: "m-g", Binary: testkit.FixturePlugin(testkit.FixturePass),
		TimeoutMs: 50, MemoryLimitPages: 1, PoolSize: 1, Logger: discardLogger()}); err != nil {
		t.Fatal(err)
	}
	b.Reset()
	metrics.RenderTo(&b)
	out = b.String()
	for _, want := range []string{
		"# TYPE muniu_plugin_instances gauge",
		`muniu_plugin_instances{plugin="`, `state="live"`, `state="idle"`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("实例仪表缺 %q:\n%s", want, out)
		}
	}
}

func TestPluginMetrics_禁用零输出(t *testing.T) {
	metrics.SetEnabled(false)
	t.Cleanup(func() { metrics.SetEnabled(false) })
	p := compileFor(t, "m-off", testkit.FixturePass)
	_ = p.HookRequestBody(context.Background(), testkit.FixtureChatBody("m"))
	var b bytes.Buffer
	metrics.RenderTo(&b)
	if strings.Contains(b.String(), "muniu_plugin") {
		t.Fatalf("禁用时不应产出插件指标: %s", b.String())
	}
}
