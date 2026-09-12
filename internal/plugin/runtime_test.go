// 宿主加载与调用测试(任务 2.2/3.1/3.2,先红后绿):
// 加载/版本探测/坏插件拒载、并发池、超时/trap/超内存/exit 四态 containment、宿主导入联调。
package plugin

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/solvela/muniu/internal/testkit"
)

func testOptions(name string, kind testkit.PluginFixtureKind) Options {
	return Options{
		Name:             name,
		Binary:           testkit.FixturePlugin(kind),
		TimeoutMs:        DefaultTimeoutMs,
		MemoryLimitPages: 1, // 测试用最小上限:越界写必触 containment
		PoolSize:         2,
		IdleTTLMs:        DefaultIdleTTLMs,
		Config:           map[string]string{"greeting": "你好"},
	}
}

func compileFor(t *testing.T, name string, kind testkit.PluginFixtureKind) *Plugin {
	t.Helper()
	p, err := Compile(testOptions(name, kind))
	if err != nil {
		t.Fatalf("Compile(%s): %v", name, err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p
}

// ---- 2.2 加载/版本探测/拒载 ----

func TestCompile_ValidPlugins(t *testing.T) {
	for _, kind := range []testkit.PluginFixtureKind{testkit.FixturePass, testkit.FixtureModify, testkit.FixtureReject, testkit.FixtureCfg} {
		p := compileFor(t, "valid-"+string(rune('a'+kind)), kind)
		if p.Name() == "" {
			t.Fatal("插件名为空")
		}
	}
}

func TestCompile_RejectNonWasm(t *testing.T) {
	_, err := Compile(Options{Name: "bad", Binary: []byte("this is not wasm at all")})
	if err == nil || !strings.Contains(err.Error(), "wasm") {
		t.Fatalf("非 wasm 二进制应拒载,得到: %v", err)
	}
}

func TestCompile_RejectTruncatedMagic(t *testing.T) {
	_, err := Compile(Options{Name: "bad", Binary: []byte{0x00, 0x61}})
	if err == nil {
		t.Fatal("截断二进制应拒载")
	}
}

func TestCompile_RejectAbiMismatch(t *testing.T) {
	_, err := Compile(testOptions("mismatch", testkit.FixtureAbiMismatch))
	if err == nil || !strings.Contains(err.Error(), "ABI") {
		t.Fatalf("ABI 版本不匹配应拒载,得到: %v", err)
	}
}

func TestCompile_RejectStartFail(t *testing.T) {
	_, err := Compile(testOptions("startfail", testkit.FixtureStartFail))
	if err == nil || !strings.Contains(err.Error(), "plugin_start") {
		t.Fatalf("plugin_start 失败应拒载,得到: %v", err)
	}
}

func TestCompile_RejectMissingExport(t *testing.T) {
	_, err := Compile(testOptions("missing", testkit.FixtureMissingExport))
	if err == nil {
		t.Fatal("缺 plugin_result 导出应拒载")
	}
}

// ---- 钩子三态 ----

func TestHook_Pass(t *testing.T) {
	p := compileFor(t, "hook-pass", testkit.FixturePass)
	v := p.HookRequestBody(context.Background(), testkit.FixtureChatBody("m1"))
	if v.Kind != VerdictKindPass {
		t.Fatalf("期望放行,得到 %v", v.Kind)
	}
}

func TestHook_Modify(t *testing.T) {
	p := compileFor(t, "hook-modify", testkit.FixtureModify)
	v := p.HookRequestBody(context.Background(), testkit.FixtureChatBody("m1"))
	if v.Kind != VerdictKindModified {
		t.Fatalf("期望改写,得到 %v", v.Kind)
	}
	if string(v.Body) != "MASKED" {
		t.Fatalf("改写结果不符: %q", v.Body)
	}
}

func TestHook_Reject(t *testing.T) {
	p := compileFor(t, "hook-reject", testkit.FixtureReject)
	v := p.HookRequestBody(context.Background(), testkit.FixtureChatBody("m1"))
	if v.Kind != VerdictKindRejected {
		t.Fatalf("期望拒绝,得到 %v", v.Kind)
	}
	if v.Reason != "pii detected" {
		t.Fatalf("拒绝理由不符: %q", v.Reason)
	}
}

// testkit.FixtureCfg:宿主导入联调——get_config 读到 "你好"(UTF-8 透传)即改写为配置值,plugin_start 经 log 上报。
func TestHook_HostImports(t *testing.T) {
	p := compileFor(t, "hook-cfg", testkit.FixtureCfg)
	v := p.HookRequestBody(context.Background(), testkit.FixtureChatBody("m1"))
	if v.Kind != VerdictKindModified {
		t.Fatalf("期望改写(get_config 应读到 greeting),得到 %v", v.Kind)
	}
	if string(v.Body) != "你好" {
		t.Fatalf("配置值透传不符: %q", v.Body)
	}
}

// ---- 3.2 资源隔离四态(超时/trap/超内存/exit)——插件死不拖垮宿主 ----

func TestContain_Timeout(t *testing.T) {
	p, err := Compile(Options{Name: "loop", Binary: testkit.FixturePlugin(testkit.FixtureLoop), TimeoutMs: 80,
		MemoryLimitPages: 1, PoolSize: 1, IdleTTLMs: DefaultIdleTTLMs})
	if err != nil {
		t.Fatalf("加载: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	for i := 0; i < 2; i++ { // 连续两次超时,网关(宿主)持续存活可继续服务
		start := time.Now()
		v := p.HookRequestBody(context.Background(), testkit.FixtureChatBody("m1"))
		if v.Kind != VerdictKindFailed {
			t.Fatalf("第 %d 次调用期望失败,得到 %v", i, v.Kind)
		}
		if time.Since(start) > 3*time.Second {
			t.Fatalf("超时未生效: %v", time.Since(start))
		}
	}
}

func TestContain_Trap(t *testing.T) {
	p := compileFor(t, "trap", testkit.FixtureTrap)
	v := p.HookRequestBody(context.Background(), testkit.FixtureChatBody("m1"))
	if v.Kind != VerdictKindFailed {
		t.Fatalf("期望失败,得到 %v", v.Kind)
	}
	// trap 后实例被丢弃,后续请求由重建实例正常服务
	v2 := p.HookRequestBody(context.Background(), testkit.FixtureChatBody("m1"))
	if v2.Kind != VerdictKindFailed {
		t.Fatalf("第二次也应失败(trap 恒定),得到 %v", v2.Kind)
	}
}

func TestContain_OOM(t *testing.T) {
	p := compileFor(t, "oom", testkit.FixtureOOM)
	v := p.HookRequestBody(context.Background(), testkit.FixtureChatBody("m1"))
	if v.Kind != VerdictKindFailed {
		t.Fatalf("期望失败,得到 %v", v.Kind)
	}
	if v.Kind == VerdictKindFailed && !strings.Contains(v.Reason, string(ContainOOM)) {
		t.Fatalf("越界写应分类为超内存 containment,得到: %q", v.Reason)
	}
}

func TestContain_Exit(t *testing.T) {
	p := compileFor(t, "exit", testkit.FixtureExit)
	v := p.HookRequestBody(context.Background(), testkit.FixtureChatBody("m1"))
	if v.Kind != VerdictKindFailed {
		t.Fatalf("期望失败(proc_exit),得到 %v", v.Kind)
	}
}

// ---- fail_open:调用失败时原请求放行(策略在 HookRequestBody 内兑现) ----

func TestFailOpen_OnContainment(t *testing.T) {
	o := testOptions("loop-open", testkit.FixtureLoop)
	o.FailOpen = true
	o.TimeoutMs = 80
	p, err := Compile(o)
	if err != nil {
		t.Fatalf("加载: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	v := p.HookRequestBody(context.Background(), testkit.FixtureChatBody("m1"))
	if v.Kind != VerdictKindPass {
		t.Fatalf("fail_open 下 containment 应放行,得到 %v", v.Kind)
	}
}

// ---- 3.1 实例池并发与回收 ----

func TestPool_ConcurrentReuse(t *testing.T) {
	p := compileFor(t, "pool", testkit.FixturePass)
	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v := p.HookRequestBody(context.Background(), testkit.FixtureChatBody("m1"))
			if v.Kind != VerdictKindPass {
				errs <- nil
			}
		}()
	}
	wg.Wait()
	close(errs)
	for range errs {
		t.Fatal("并发调用出现非放行结果")
	}
	if created := p.pool.createdTotal.Load(); created < 1 {
		t.Fatalf("实例创建计数异常: %d", created)
	}
}

func TestPool_IdleRecycle(t *testing.T) {
	o := testOptions("idle", testkit.FixturePass)
	o.IdleTTLMs = 30
	p, err := Compile(o)
	if err != nil {
		t.Fatalf("加载: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	if v := p.HookRequestBody(context.Background(), testkit.FixtureChatBody("m1")); v.Kind != VerdictKindPass {
		t.Fatalf("首次调用应放行")
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if p.pool.closedTotal.Load() > 0 { // 空闲回收已发生
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("空闲实例未被回收")
}

// ---- 拒载后既有插件不受影响(spec「非法插件拒载」场景) ----

func TestCompile_RejectKeepsExisting(t *testing.T) {
	p := compileFor(t, "keeper", testkit.FixturePass)
	if _, err := Compile(testOptions("keeper-bad", testkit.FixtureAbiMismatch)); err == nil {
		t.Fatal("坏插件应拒载")
	}
	if v := p.HookRequestBody(context.Background(), testkit.FixtureChatBody("m1")); v.Kind != VerdictKindPass {
		t.Fatal("拒载后既有插件行为应不受影响")
	}
}

// free 中死循环:宿主必须有界中断(free 独立超时),调用按 containment 终结,
// 网关持续存活——恶意插件在清理环节的挂死链回归钉子。
func TestContain_FreeLoop(t *testing.T) {
	o := testOptions("free-loop", testkit.FixtureFreeLoop)
	o.TimeoutMs = 100
	p, err := Compile(o)
	if err != nil {
		t.Fatalf("加载: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	start := time.Now()
	v := p.HookRequestBody(context.Background(), testkit.FixtureChatBody("m"))
	if v.Kind != VerdictKindFailed {
		t.Fatalf("free 死循环应按 containment 终结,实际 %v", v.Kind)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("free 超时未生效: %v", elapsed)
	}
}

// discardLogger 测试用静音日志器。
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// ---- 注册探测超时:plugin_start 死循环必须在有界时间内拒载 ----

func TestCompile_ProbeTimeout(t *testing.T) {
	o := testOptions("start-loop", testkit.FixtureStartLoop)
	o.TimeoutMs = 50       // 探测超时缺省 = max(2s, 4×TimeoutMs) = 2s
	o.ProbeTimeoutMs = 100 // 测试收紧
	start := time.Now()
	_, err := Compile(o)
	if err == nil {
		t.Fatal("plugin_start 死循环必须探测超时拒载")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("探测超时未生效: %v", elapsed)
	}
	if !strings.Contains(err.Error(), "探测超时") && !strings.Contains(err.Error(), "deadline") &&
		!strings.Contains(err.Error(), "context") {
		t.Fatalf("错误应体现探测超时: %v", err)
	}
}
