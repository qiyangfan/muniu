// Package testkit 提供代理和插件测试使用的固定替身。
package testkit

import (
	"github.com/solvela/muniu/internal/adapter"
	"github.com/solvela/muniu/internal/adapter/bridge"
)

// FakeIncompleteBridge 未声明工具翻译完整的桥接替身，嵌入真实 anthropic 桥接，翻译行为真实。
type FakeIncompleteBridge struct {
	*bridge.Anthropic
}

// NewFakeIncompleteBridge 创建使用真实 Anthropic 翻译逻辑的测试替身。
func NewFakeIncompleteBridge(baseURL string) *FakeIncompleteBridge {
	return &FakeIncompleteBridge{Anthropic: bridge.NewAnthropic(baseURL, adapter.Options{})}
}

// BridgeKind 覆写：换名防测试间混淆。
func (f *FakeIncompleteBridge) BridgeKind() string { return "fake-incomplete" }

// ToolRoundtripComplete 返回 false，表示工具往返翻译能力未经声明。
func (f *FakeIncompleteBridge) ToolRoundtripComplete() bool { return false }
