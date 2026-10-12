// 假桥接替身(anthropic-bridge-tool-roundtrip):真实桥接型已全部声明工具翻译完整,
// bridgegate 豁免机制转为防御性保留——本替身(Bridger+保守缺省 false)是豁免语义的
// 钉子载体,防机制被误删(未来桥接型的保守缺省依赖它)。
package testkit

import (
	"github.com/solvela/muniu/internal/adapter"
	"github.com/solvela/muniu/internal/adapter/bridge"
)

// FakeIncompleteBridge 未声明工具翻译完整的桥接替身(嵌入真实 anthropic 桥接,翻译行为真实)。
type FakeIncompleteBridge struct {
	*bridge.Anthropic
}

// NewFakeIncompleteBridge 以真实 anthropic 桥接为底(翻译行为真实),仅覆写两项声明:
// BridgeKind 换名防测试间混淆;ToolRoundtripComplete 如实回落保守缺省 false。
func NewFakeIncompleteBridge(baseURL string) *FakeIncompleteBridge {
	return &FakeIncompleteBridge{Anthropic: bridge.NewAnthropic(baseURL, adapter.Options{})}
}

// BridgeKind 覆写:换名防测试间混淆。
func (f *FakeIncompleteBridge) BridgeKind() string { return "fake-incomplete" }

// ToolRoundtripComplete 覆写:回落保守缺省 false(豁免语义的钉子载体)。
func (f *FakeIncompleteBridge) ToolRoundtripComplete() bool { return false }
