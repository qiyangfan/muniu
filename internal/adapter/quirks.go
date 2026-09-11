// quirk patch 实现(ADR-0003):只声明「与 openai 基座的差异」,禁止整家覆写。
// 每个差异点溯源到 conformance fixture 与官方文档(见 fixture source_url)。
// 已核对结论(docs/design/upstream-adapters.md 矩阵,2026-09-09):
//   - deepseek:usage 缓存命中为顶层 prompt_cache_hit_tokens(非 OpenAI details 嵌套),
//     reasoning_tokens 在 completion_tokens_details;
//   - moonshot:cached_tokens 为顶层 OpenAI 风格命名,流式 usage 依赖 include_usage(缺省无 usage 帧);
//   - glm:当前流式形态与基座无已实证差异 → 显式空 quirk(流式 usage 返回方式仍 ⚠ 待实证)。
package adapter

import (
	"encoding/json"
	"fmt"

	"github.com/solvela/muniu/internal/failover"
	"github.com/solvela/muniu/internal/protocol"
)

// quirk 通用包装:默认全透传基座,仅覆写声明了差异的方法。
type quirk struct {
	base           Adapter
	normalizeUsage func(raw json.RawMessage) (protocol.Usage, error)
}

func (q *quirk) Prepare(in *protocol.ChatRequest, upstreamModel, apiKey string) (*Outbound, error) {
	return q.base.Prepare(in, upstreamModel, apiKey)
}

func (q *quirk) Classify(status int, body []byte) failover.ErrorClass {
	return q.base.Classify(status, body)
}

func (q *quirk) StreamEvent(data []byte) ([]byte, EventMeta, error) {
	return q.base.StreamEvent(data)
}

func (q *quirk) RewriteResponse(body []byte) ([]byte, json.RawMessage, error) {
	return q.base.RewriteResponse(body)
}

func (q *quirk) NormalizeUsage(raw json.RawMessage) (protocol.Usage, error) {
	return q.normalizeUsage(raw)
}

// newDeepseek DeepSeek quirk:缓存命中字段重映射(见 deepseek/usage-cache-tokens fixture)。
func newDeepseek(base Adapter) Adapter {
	return &quirk{base: base, normalizeUsage: func(raw json.RawMessage) (protocol.Usage, error) {
		var u struct {
			PromptTokens      int64 `json:"prompt_tokens"`
			CompletionTokens  int64 `json:"completion_tokens"`
			PromptCacheHit    int64 `json:"prompt_cache_hit_tokens"`
			PromptCacheMiss   int64 `json:"prompt_cache_miss_tokens"`
			CompletionDetails struct {
				ReasoningTokens int64 `json:"reasoning_tokens"`
			} `json:"completion_tokens_details"`
		}
		if err := json.Unmarshal(raw, &u); err != nil {
			return protocol.Usage{}, fmt.Errorf("adapter: 解析 deepseek usage: %w", err)
		}
		return protocol.Usage{
			PromptTokens:     u.PromptTokens,
			CompletionTokens: u.CompletionTokens,
			ReasoningTokens:  u.CompletionDetails.ReasoningTokens,
			CachedTokens:     u.PromptCacheHit,
		}, nil
	}}
}

// newMoonshot Moonshot/Kimi quirk:缓存命中为顶层 cached_tokens(OpenAI 风格命名但位置不同)。
func newMoonshot(base Adapter) Adapter {
	return &quirk{base: base, normalizeUsage: func(raw json.RawMessage) (protocol.Usage, error) {
		var u struct {
			PromptTokens     int64 `json:"prompt_tokens"`
			CompletionTokens int64 `json:"completion_tokens"`
			CachedTokens     int64 `json:"cached_tokens"`
		}
		if err := json.Unmarshal(raw, &u); err != nil {
			return protocol.Usage{}, fmt.Errorf("adapter: 解析 moonshot usage: %w", err)
		}
		return protocol.Usage{
			PromptTokens:     u.PromptTokens,
			CompletionTokens: u.CompletionTokens,
			CachedTokens:     u.CachedTokens,
		}, nil
	}}
}

// newGLM GLM quirk:当前无与基座的已实证差异(显式空 quirk,全方法透传基座)。
// 流式 usage 返回方式仍 ⚠ 待实证;实证后差异在此落地并配 fixture。
func newGLM(base Adapter) Adapter {
	return &quirk{base: base, normalizeUsage: base.NormalizeUsage}
}
