// 桥接工具多轮显式边界(upstream-egress-batch「anthropic-bridge」):
// 请求含 role=tool 或 assistant.tool_calls(多轮中途标记)时,桥接候选按适配性豁免
// 处理——多候选别名换道跳过,仅桥接可达时返回自解释 400 信封,消除「透传让上游
// 400 再以 upstream_error 冒出」的不诚实失败。首轮 tools 声明不触发(响应侧翻译完整)。
package proxy

import (
	"encoding/json"

	"github.com/solvela/muniu/internal/adapter"
	"github.com/solvela/muniu/internal/protocol"
)

// bridgeToolUnsupportedCode 桥接工具多轮不可服务的专用机器码。
const bridgeToolUnsupportedCode = "bridge_tool_roundtrip_unsupported"

// chatToolLoopInflight 多轮中途标记检测:messages 含 role=tool,或任一 assistant
// 消息带非空 tool_calls。仅声明 tools 参数(无中途标记)不触发——响应侧 tool_use
// 翻译完整,首轮合法。解析失败按 false(入站校验已拒非法体,此处宁放行不误伤)。
func chatToolLoopInflight(req *protocol.ChatRequest) bool {
	var envelope struct {
		Messages []struct {
			Role      string          `json:"role"`
			ToolCalls json.RawMessage `json:"tool_calls"`
		} `json:"messages"`
	}
	if json.Unmarshal(req.Raw, &envelope) != nil {
		return false
	}
	for _, m := range envelope.Messages {
		if m.Role == "tool" {
			return true
		}
		if len(m.ToolCalls) > 0 && string(m.ToolCalls) != "null" && string(m.ToolCalls) != "[]" {
			return true
		}
	}
	return false
}

// bridgeExempted 桥接候选适配性豁免判定:多轮中途流量 × 桥接型适配器。
// 豁免发生在取 key 之前——不消耗冷却/EMA(适配性不匹配不是上游故障,
// 与 adapter_missing 早退、strict 治理拒绝同一裁决口径)。
func bridgeExempted(ad adapter.Adapter, midLoop bool) bool {
	if !midLoop {
		return false
	}
	_, ok := ad.(adapter.Bridger)
	return ok
}
