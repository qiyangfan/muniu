// quirk patch 只声明与 OpenAI 基座的差异；每项差异均对应 conformance fixture。
//   - deepseek：usage 缓存命中为顶层 prompt_cache_hit_tokens（非 OpenAI 的 details 嵌套），reasoning_tokens 在 completion_tokens_details；
//   - moonshot：cached_tokens 为顶层 OpenAI 风格命名，流式 usage 依赖 include_usage，缺省无 usage 帧；
//   - glm：流式形态与基座无已实证差异，流式 usage 返回方式仍待实证。
package adapter

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/solvela/muniu/internal/failover"
	"github.com/solvela/muniu/internal/protocol"
)

// quirk 通用包装：默认透传基座，仅覆写声明了差异的方法。
type quirk struct {
	base           Adapter
	normalizeUsage func(raw json.RawMessage) (protocol.Usage, error)
	classify       func(status int, body []byte) failover.ErrorClass
}

func (q *quirk) Prepare(in *protocol.ChatRequest, upstreamModel, apiKey string) (*Outbound, error) {
	return q.base.Prepare(in, upstreamModel, apiKey)
}

func (q *quirk) Classify(status int, body []byte) failover.ErrorClass {
	if q.classify != nil {
		return q.classify(status, body)
	}
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

// newDeepseek 添加 DeepSeek usage 字段归一化和 402 余额不足分类。
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
	}, classify: func(status int, body []byte) failover.ErrorClass {
		// 402 欠费是充值即恢复的暂态，归入限流冷却走指数退避与自动重探，
		// 而非 401 式禁用；限流冷却对应熔断器半开自动探测，禁用则需人工重启恢复。
		if status == http.StatusPaymentRequired {
			return failover.ClassRateLimited
		}
		return base.Classify(status, body)
	}}
}

// newMoonshot 添加 Moonshot/Kimi quirk：缓存命中为顶层 cached_tokens，命名同 OpenAI 但位置不同。
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

// newGLM 保留 GLM 的 quirk 扩展点；当前所有方法均透传基座。
// 流式 usage 返回方式待实证。
func newGLM(base Adapter) Adapter {
	return &quirk{base: base, normalizeUsage: base.NormalizeUsage}
}
