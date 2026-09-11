// 请求参数治理(ADR-0010,upstream-adapters「请求参数治理」节):声明制、逐 attempt、留痕放行。
// 治理在 OpenAI 统一形态的 Raw 字节层操作(RewriteModel 同款 map 模式,未知字段零丢失),
// 并同步已解析结构字段(桥接消费 in.MaxTokens/in.Temperature);原始请求永不被触碰——
// 每次 attempt 从原件重新治理,换道后按新 (provider, model) 的约束生效(spec 换道重治理场景)。
package proxy

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/solvela/muniu/internal/config"
	"github.com/solvela/muniu/internal/protocol"
)

// governChange 单次干预记录(留痕双通道的共同数据源:结构化日志明细 + 响应头参数清单)。
type governChange struct {
	Param  string `json:"param"`
	Action string `json:"action"` // drop | clamp | thinking_lock
	From   string `json:"from,omitempty"`
	To     string `json:"to,omitempty"`
}

// governKey 能力条目键:(provider, model) 二元组,零通配(显式、可审计,design 决策 3)。
type governKey struct{ provider, model string }

// governChat 对入站请求执行能力治理:capability 为该目标的声明约束,strict=true 时命中即拒绝。
// 返回治理后请求(副本;零干预返回原件)、干预明细、strict 拒绝消息。
func governChat(req *protocol.ChatRequest, capability config.ModelCapability, strict bool) (*protocol.ChatRequest, []governChange, string) {
	if len(capability.DropParams) == 0 && !capability.ThinkingLock && capability.MaxOutputTokens <= 0 {
		return req, nil, "" // 未声明任何约束:零干预快速路径
	}

	// ParseChatRequest 已保证合法 JSON;解析失败防御性原样放行(治理不得引入新失败面)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(req.Raw, &fields); err != nil {
		return req, nil, ""
	}

	var changes []governChange
	var rejected []string

	// drop:黑名单参数整体移除
	dropped := map[string]bool{}
	for _, name := range capability.DropParams {
		raw, ok := fields[name]
		if !ok {
			continue
		}
		delete(fields, name)
		dropped[name] = true
		changes = append(changes, governChange{Param: name, Action: "drop", From: summarize(raw)})
		if strict {
			rejected = append(rejected, fmt.Sprintf("%s(drop: 模型声明不支持该参数)", name))
		}
	}

	// clamp:输出上限收敛(仅 max_tokens;界内零干预)
	if capability.MaxOutputTokens > 0 {
		if raw, ok := fields["max_tokens"]; ok {
			var v int64
			if err := json.Unmarshal(raw, &v); err == nil && v > capability.MaxOutputTokens {
				fields["max_tokens"], _ = json.Marshal(capability.MaxOutputTokens)
				changes = append(changes, governChange{Param: "max_tokens", Action: "clamp",
					From: strconv.FormatInt(v, 10), To: strconv.FormatInt(capability.MaxOutputTokens, 10)})
				if strict {
					rejected = append(rejected, fmt.Sprintf("max_tokens(clamp: 模型输出上限 %d)", capability.MaxOutputTokens))
				}
			}
		}
	}

	// thinking_lock:移除「关闭思考」指令,模型保持默认思考形态(即其允许的最小思考)
	if capability.ThinkingLock {
		for _, name := range thinkingOffDialects(fields) {
			raw := fields[name]
			delete(fields, name)
			changes = append(changes, governChange{Param: name, Action: "thinking_lock", From: summarize(raw)})
			if strict {
				rejected = append(rejected, fmt.Sprintf("%s(thinking_lock: 模型思考不可关闭)", name))
			}
		}
	}

	if strict && len(rejected) > 0 {
		// 拒绝消息指明目标模型(spec「strict 模式」场景:信封须指明模型、参数与约束原因)
		return nil, nil, fmt.Sprintf("请求参数与模型能力约束冲突[%s/%s](strict_governance): %s",
			capability.Provider, capability.Model, strings.Join(rejected, "; "))
	}
	if len(changes) == 0 {
		return req, nil, ""
	}

	out, err := json.Marshal(fields)
	if err != nil {
		return req, nil, ""
	}
	governed := *req // 浅副本:Messages/Tools 只读共享,治理不触碰
	governed.Raw = out
	if dropped["temperature"] {
		governed.Temperature = nil
	}
	if dropped["max_tokens"] {
		governed.MaxTokens = nil
	}
	if capability.MaxOutputTokens > 0 && governed.MaxTokens != nil && *governed.MaxTokens > capability.MaxOutputTokens {
		governed.MaxTokens = new(int64)
		*governed.MaxTokens = capability.MaxOutputTokens
	}
	return &governed, changes, ""
}

// thinkingOffDialects 识别「关闭思考」指令的三家已知方言(qwen 系 enable_thinking、
// OpenAI reasoning_effort=minimal、GLM thinking.type=disabled)。这是协议形态识别而非能力内置:
// 识别客户端如何表达意图,与「哪些模型受何约束」的目录声明(零内置)正交。
func thinkingOffDialects(fields map[string]json.RawMessage) []string {
	var out []string
	if raw, ok := fields["enable_thinking"]; ok {
		var b bool
		if json.Unmarshal(raw, &b) == nil && !b {
			out = append(out, "enable_thinking")
		}
	}
	if raw, ok := fields["reasoning_effort"]; ok {
		var s string
		if json.Unmarshal(raw, &s) == nil && s == "minimal" {
			out = append(out, "reasoning_effort")
		}
	}
	if raw, ok := fields["thinking"]; ok {
		var t struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(raw, &t) == nil && t.Type == "disabled" {
			out = append(out, "thinking")
		}
	}
	return out
}

// summarize 留痕用的原值摘要:32 字节目标,rune 边界回退——字节硬切会把多字节字符
// 劈成非法 UTF-8(经 json.Marshal 变 U+FFFD),同 splitForReplay 的教训。
func summarize(raw json.RawMessage) string {
	s := string(raw)
	if len(s) <= 32 {
		return s
	}
	end := 32
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end] + "…"
}
