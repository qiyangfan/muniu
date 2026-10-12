// 同构直通路径判定表驱动测试(protocol-native-passthrough;测试先行域):
// 每 attempt 判定为纯函数——入站标签 × 候选能力声明 × 回落信号(native/translated/
// 失格升格)全格枚举;判定派生自显式声明,禁止请求体协议探测。
package proxy

import (
	"encoding/json"
	"testing"

	"github.com/solvela/muniu/internal/adapter"
	"github.com/solvela/muniu/internal/config"
	"github.com/solvela/muniu/internal/failover"
	"github.com/solvela/muniu/internal/protocol"
)

// stubPlain 最小适配器桩(仅 Adapter 五方法,无任何可选能力):直通无能力格的对照物。
type stubPlain struct{}

func (stubPlain) Prepare(*protocol.ChatRequest, string, string) (*adapter.Outbound, error) {
	return nil, nil
}
func (stubPlain) Classify(int, []byte) failover.ErrorClass { return failover.ClassUnknown }
func (stubPlain) NormalizeUsage(raw json.RawMessage) (protocol.Usage, error) {
	return protocol.Usage{}, nil
}
func (stubPlain) StreamEvent([]byte) ([]byte, adapter.EventMeta, error) {
	return nil, adapter.EventMeta{}, nil
}
func (stubPlain) RewriteResponse([]byte) ([]byte, json.RawMessage, error) {
	return nil, nil, nil
}

// stubNative 直通能力桩(协议身份可注入,声明表另行门控)。
type stubNative struct {
	stubPlain
	proto protocol.InboundProtocol
}

func (s stubNative) NativeProto() protocol.InboundProtocol { return s.proto }
func (stubNative) PrepareNative(*protocol.NativeRequest, string, string) (*adapter.Outbound, error) {
	return nil, nil
}
func (stubNative) NativeStreamEvent([]byte) ([]byte, adapter.EventMeta, error) {
	return nil, adapter.EventMeta{}, nil
}
func (stubNative) NormalizeNativeUsage(raw json.RawMessage) (protocol.Usage, error) {
	return protocol.Usage{}, nil
}

// TestDecideNativePath 判定表:每格 = (入站标签, 候选适配器能力, native_responses 声明, 插件改写) → 路径。
func TestDecideNativePath(t *testing.T) {
	cand := failover.Candidate{Provider: "p1", Model: "m1"}

	msgAdapter := map[string]adapter.Adapter{"p1": stubNative{proto: protocol.InboundMessages}}
	cases := []struct {
		name           string
		nat            *inboundNative
		adapters       map[string]adapter.Adapter
		nativeResps    map[string]bool
		pluginModified bool
		wantNative     bool
		wantFallback   string
	}{
		{"chat入站恒翻译", nil, msgAdapter, nil, false, false, ""},
		{"messages×直通能力=直通", &inboundNative{proto: protocol.InboundMessages}, msgAdapter, nil, false, true, ""},
		{"messages×无能力=翻译", &inboundNative{proto: protocol.InboundMessages}, map[string]adapter.Adapter{"p1": stubPlain{}}, nil, false, false, ""},
		{"messages×协议身份不符=翻译", &inboundNative{proto: protocol.InboundMessages}, map[string]adapter.Adapter{"p1": stubNative{proto: protocol.InboundResponses}}, map[string]bool{"p1": true}, false, false, ""},
		{"responses×声明=直通", &inboundNative{proto: protocol.InboundResponses}, map[string]adapter.Adapter{"p1": stubNative{proto: protocol.InboundResponses}}, map[string]bool{"p1": true}, false, true, ""},
		{"responses×未声明=翻译兜底", &inboundNative{proto: protocol.InboundResponses}, map[string]adapter.Adapter{"p1": stubNative{proto: protocol.InboundResponses}}, map[string]bool{"p1": false}, false, false, ""},
		{"responses×无能力=翻译兜底", &inboundNative{proto: protocol.InboundResponses}, map[string]adapter.Adapter{"p1": stubPlain{}}, map[string]bool{"p1": true}, false, false, ""},
		{"插件改写回落翻译", &inboundNative{proto: protocol.InboundMessages}, msgAdapter, nil, true, false, "plugin_modified"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := decideNativePath(c.nat, c.adapters, c.nativeResps, cand, c.pluginModified)
			if got.native != c.wantNative || got.fallback != c.wantFallback {
				t.Fatalf("判定不符: got={native:%v fallback:%q} want={native:%v fallback:%q}",
					got.native, got.fallback, c.wantNative, c.wantFallback)
			}
		})
	}
	// 失格请求不影响判定本身(升格在 attempt 出站前,属路径执行而非判定)
	got := decideNativePath(&inboundNative{proto: protocol.InboundMessages, ineligCode: protocol.CodeContentBlockUnsupported},
		map[string]adapter.Adapter{"p1": stubNative{proto: protocol.InboundMessages}}, nil, cand, false)
	if !got.native {
		t.Fatal("失格请求在直通候选上仍应判直通(惰性拒绝)")
	}
}

// TestBuildNativeResps 声明随快照:native_responses 声明驱动 responses 直通资格。
func TestBuildNativeResps(t *testing.T) {
	m := buildNativeResps([]config.Provider{
		{Name: "declared", NativeResponses: true},
		{Name: "silent"},
	})
	if !m["declared"] || m["silent"] {
		t.Fatalf("声明表不符: %+v", m)
	}
}

// TestAliasHasNative 别名级预判(惰性拒绝的即时 400 判据)。
func TestAliasHasNative(t *testing.T) {
	snap := &topologySnapshot{
		adapters: map[string]adapter.Adapter{
			"anth": stubNative{proto: protocol.InboundMessages},
			"p":    stubPlain{},
			"decl": stubNative{proto: protocol.InboundResponses},
		},
		nativeResps: map[string]bool{"decl": true},
	}
	if !aliasHasNative(snap, protocol.InboundMessages, []config.Candidate{{Provider: "p"}, {Provider: "anth"}}) {
		t.Fatal("含直通候选的别名应预判为可直通")
	}
	if aliasHasNative(snap, protocol.InboundMessages, []config.Candidate{{Provider: "p"}}) {
		t.Fatal("无直通候选的别名应预判为不可直通")
	}
	if !aliasHasNative(snap, protocol.InboundResponses, []config.Candidate{{Provider: "decl"}}) {
		t.Fatal("声明候选应预判为可直通")
	}
	if aliasHasNative(snap, protocol.InboundResponses, []config.Candidate{{Provider: "anth"}}) {
		t.Fatal("未声明候选不得预判为 responses 直通")
	}
}
