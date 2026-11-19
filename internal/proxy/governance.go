// 参数约束按 provider 和 model 声明，并在每次候选尝试时应用。
// 治理改写请求副本并保留未知字段；换道后从原始请求按新候选的约束重新处理。
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

// governChange 记录一次参数干预，供日志和响应头共用。
type governChange struct {
	Param  string `json:"param"`
	Action string `json:"action"` // drop | clamp | thinking_lock
	From   string `json:"from,omitempty"`
	To     string `json:"to,omitempty"`
}

// governKey 是能力目录的 provider 和 model 组合键。
type governKey struct{ provider, model string }

// governChat 按目标模型的能力约束处理请求；strict 模式下命中约束即拒绝。
// 返回处理后的请求、干预记录和拒绝原因；未改写时返回原请求。
func governChat(req *protocol.ChatRequest, capability config.ModelCapability,
	strict bool) (*protocol.ChatRequest, []governChange, string) {
	if len(capability.DropParams) == 0 && !capability.ThinkingLock && capability.MaxOutputTokens <= 0 {
		return req, nil, "" // 未声明任何约束：零干预快速路径
	}

	// ParseChatRequest 已保证合法 JSON，解析失败防御性原样放行，治理不得引入新失败面
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(req.Raw, &fields); err != nil {
		return req, nil, ""
	}

	g := &governRun{capability: capability, strict: strict, fields: fields}
	g.dropDeclared()
	g.clampMaxTokens()
	g.lockThinking()

	if g.strict && len(g.rejected) > 0 {
		return nil, nil, fmt.Sprintf("请求参数与模型能力约束冲突[%s/%s](strict_governance): %s",
			capability.Provider, capability.Model, strings.Join(g.rejected, "; "))
	}
	if len(g.changes) == 0 {
		return req, nil, ""
	}

	out, err := json.Marshal(fields)
	if err != nil {
		return req, nil, ""
	}
	governed := *req // 浅副本：Messages/Tools 只读共享，治理不触碰
	governed.Raw = out
	g.applyTo(&governed)
	return &governed, g.changes, ""
}

// governRun 单次治理的累积状态：干预记录、strict 拒绝清单与已丢弃参数集合。
type governRun struct {
	capability config.ModelCapability
	strict     bool
	fields     map[string]json.RawMessage
	changes    []governChange
	rejected   []string
	dropped    map[string]bool
}

// dropDeclared 丢弃模型声明不支持的参数。
func (g *governRun) dropDeclared() {
	g.dropped = map[string]bool{}
	for _, name := range g.capability.DropParams {
		raw, ok := g.fields[name]
		if !ok {
			continue
		}
		delete(g.fields, name)
		g.dropped[name] = true
		g.changes = append(g.changes, governChange{Param: name, Action: "drop", From: summarize(raw)})
		if g.strict {
			g.rejected = append(g.rejected, fmt.Sprintf("%s(drop: 模型声明不支持该参数)", name))
		}
	}
}

// clampMaxTokens 输出上限钳制：仅 max_tokens 超声明值时改写。
func (g *governRun) clampMaxTokens() {
	limit := g.capability.MaxOutputTokens
	if limit <= 0 {
		return
	}
	raw, ok := g.fields["max_tokens"]
	if !ok {
		return
	}
	var v int64
	if err := json.Unmarshal(raw, &v); err != nil || v <= limit {
		return
	}
	g.fields["max_tokens"], _ = json.Marshal(limit)
	g.changes = append(g.changes, governChange{Param: "max_tokens", Action: "clamp",
		From: strconv.FormatInt(v, 10), To: strconv.FormatInt(limit, 10)})
	if g.strict {
		g.rejected = append(g.rejected, fmt.Sprintf("max_tokens(clamp: 模型输出上限 %d)", limit))
	}
}

// lockThinking 思考锁：模型思考不可关闭，删除方言关闭参数。
func (g *governRun) lockThinking() {
	if !g.capability.ThinkingLock {
		return
	}
	for _, name := range thinkingOffDialects(g.fields) {
		raw := g.fields[name]
		delete(g.fields, name)
		g.changes = append(g.changes, governChange{Param: name, Action: "thinking_lock", From: summarize(raw)})
		if g.strict {
			g.rejected = append(g.rejected, fmt.Sprintf("%s(thinking_lock: 模型思考不可关闭)", name))
		}
	}
}

// applyTo 把字段级改写同步到 canonical 结构体字段。
func (g *governRun) applyTo(governed *protocol.ChatRequest) {
	if g.dropped["temperature"] {
		governed.Temperature = nil
	}
	if g.dropped["max_tokens"] {
		governed.MaxTokens = nil
	}
	limit := g.capability.MaxOutputTokens
	if limit > 0 && governed.MaxTokens != nil && *governed.MaxTokens > limit {
		governed.MaxTokens = new(int64)
		*governed.MaxTokens = limit
	}
}

// thinkingOffDialects 识别客户端关闭思考时使用的参数形式；是否禁用由能力目录决定。
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

// summarize 截取参数原值用于留痕，并在 UTF-8 字符边界截断。
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
