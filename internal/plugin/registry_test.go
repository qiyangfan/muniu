// 注册表生命周期回归测试(评审修复钉子):热替换在途不扰、替换后新请求走新版本。
package plugin

import (
	"context"
	"testing"
	"time"

	"github.com/solvela/muniu/internal/testkit"
)

func testRegOptions(name string, kind testkit.PluginFixtureKind) Options {
	return Options{
		Name: name, Binary: testkit.FixturePlugin(kind), Enabled: true,
		TimeoutMs: 200, MemoryLimitPages: 1, PoolSize: 2,
		Logger: discardLogger(),
	}
}

// 热替换契约(spec「版本替换热生效」):新请求走新版本;已取快照的在途请求
// 在旧版本上完整跑完(而非被「已下线」误拒);快照归零后旧版本关闭。
func TestRegistry_HotSwap_在途不扰(t *testing.T) {
	reg := NewRegistry()
	t.Cleanup(reg.Close)
	if _, _, err := reg.Register(testRegOptions("p", testkit.FixtureModifyEcho)); err != nil {
		t.Fatal(err)
	}

	// 模拟在途请求:已取快照、尚未执行到插件
	snap := reg.acquireSnapshot()

	// 并发替换为拒绝型新版本
	if _, replaced, err := reg.Register(testRegOptions("p", testkit.FixtureReject)); err != nil || !replaced {
		t.Fatalf("替换失败: replaced=%v err=%v", replaced, err)
	}

	// 新请求走新版本(拒绝)
	res := reg.ApplyToBody(context.Background(), testkit.FixtureChatBody("m"))
	if res.RejectedBy != "p" {
		t.Fatalf("替换后新请求应走新版本(拒绝),实际 %+v", res)
	}

	// 在途快照持有者:旧版本完整执行(改写回显),不得被「已下线」误拒
	v := snap[0].HookRequestBody(context.Background(), testkit.FixtureChatBody("m"))
	if v.Kind != VerdictKindModified {
		t.Fatalf("在途请求应在旧版本上完整执行,实际 %v(%s)", v.Kind, v.Reason)
	}
	reg.releaseSnapshot(snap)

	// 快照归零后旧版本被 drain 关闭(经原子 IsClosed 轮询;已下线的插件不得再调用钩子
	// ——Close 的 inflight.Wait 与钩子的 WaitGroup.Add 并发即数据竞争)
	for deadline := time.Now().Add(3 * time.Second); !snap[0].IsClosed(); {
		if time.Now().After(deadline) {
			t.Fatal("快照归零后旧版本未被关闭")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
