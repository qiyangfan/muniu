// openai 兼容基座:覆盖所有「OpenAI 兼容端点」供应商(DeepSeek/Moonshot/GLM/Qwen/Doubao 等)。
// 差异只进 quirks patch,禁止为兼容家复制整套适配器(ADR-0003)。
package adapter

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/solvela/muniu/internal/failover"
	"github.com/solvela/muniu/internal/protocol"
)

// OpenAI 基座实例:一个 provider 一个(基址绑定;key 由 keypool 每请求提供)。
type OpenAI struct {
	baseURL string
}

// NewOpenAI 构造基座适配器(apiKey 参数保留位:基座不再持有 key,兼容旧调用传空串)。
func NewOpenAI(baseURL, apiKey string) *OpenAI {
	return &OpenAI{baseURL: baseURL}
}

// Prepare 组装出站请求:URL = base_url + /chat/completions(容忍基址尾斜杠);
// body 经 protocol.RewriteModel 只换 model;鉴权 Bearer,key 由 keypool 每请求注入。
func (o *OpenAI) Prepare(in *protocol.ChatRequest, upstreamModel, apiKey string) (*Outbound, error) {
	body, err := protocol.RewriteModel(in.Raw, upstreamModel)
	if err != nil {
		return nil, fmt.Errorf("adapter: 改写出站 body: %w", err)
	}
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("Authorization", "Bearer "+apiKey)
	return &Outbound{
		URL:    strings.TrimRight(o.baseURL, "/") + "/chat/completions",
		Header: h,
		Body:   body,
	}, nil
}

// Classify 按状态码粗分(Phase 1 粒度;body 细分留给 quirk 层需要时再启用)。
func (o *OpenAI) Classify(status int, _ []byte) failover.ErrorClass {
	switch {
	case status == http.StatusBadRequest:
		return failover.ClassBadRequest
	case status == http.StatusUnauthorized, status == http.StatusForbidden:
		return failover.ClassAuthFailed
	case status == http.StatusTooManyRequests:
		return failover.ClassRateLimited
	case status >= 500:
		return failover.ClassUpstream5xx
	default:
		return failover.ClassUnknown
	}
}

// openAIUsage OpenAI usage 原始形态(归一表的 OpenAI 列)。
type openAIUsage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	PromptDetails    struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionDetails struct {
		ReasoningTokens int64 `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

// NormalizeUsage OpenAI 列 → 统一 Usage(缺失按 0,不猜)。
func (o *OpenAI) NormalizeUsage(raw json.RawMessage) (protocol.Usage, error) {
	var raw_ openAIUsage
	if err := json.Unmarshal(raw, &raw_); err != nil {
		return protocol.Usage{}, fmt.Errorf("adapter: 解析上游 usage: %w", err)
	}
	return protocol.Usage{
		PromptTokens:     raw_.PromptTokens,
		CompletionTokens: raw_.CompletionTokens,
		ReasoningTokens:  raw_.CompletionDetails.ReasoningTokens,
		CachedTokens:     raw_.PromptDetails.CachedTokens,
	}, nil
}

// PrepareEmbeddings 组装 embeddings 出站请求(URL = base + /v1/embeddings,只换 model)。
func (o *OpenAI) PrepareEmbeddings(in *protocol.EmbeddingRequest, upstreamModel, apiKey string) (*Outbound, error) {
	body, err := protocol.RewriteModel(in.Raw, upstreamModel)
	if err != nil {
		return nil, fmt.Errorf("adapter: 改写 embeddings body: %w", err)
	}
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("Authorization", "Bearer "+apiKey)
	return &Outbound{
		URL:    strings.TrimRight(o.baseURL, "/") + "/embeddings",
		Header: h,
		Body:   body,
	}, nil
}

// RewriteResponse 基座非流式:原样透传,仅抽取 usage 原始字节供归一。
func (o *OpenAI) RewriteResponse(body []byte) ([]byte, json.RawMessage, error) {
	var resp struct {
		Usage json.RawMessage `json:"usage"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, nil, fmt.Errorf("adapter: 解析非流式响应: %w", err)
	}
	return body, resp.Usage, nil
}

// StreamEvent 只读归一:passthrough 恒为原字节(字段改写白名单),meta 供首 token 判定与计量。
func (o *OpenAI) StreamEvent(data []byte) ([]byte, EventMeta, error) {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "[DONE]" {
		return data, EventMeta{IsDone: true}, nil
	}
	if trimmed == "" || strings.HasPrefix(trimmed, ":") {
		// 空 data / 纯注释事件(SSE 心跳):对客户端不可见且不报错——按解析失败处理会把
		// 合法心跳误判为上游断流,长流在心跳点被随机切断
		return nil, EventMeta{}, nil
	}
	chunk, err := protocol.ParseStreamChunk(data)
	if err != nil {
		return nil, EventMeta{}, fmt.Errorf("adapter: 解析 SSE 帧: %w", err)
	}
	return data, EventMeta{
		HasContent: chunk.HasContent(),
		// 帧携带 usage 即为 usage 帧(Doubao 等家挂在 finish 帧上,choices 非空);
		// 纯 usage 终帧(empty choices)同样命中
		IsUsage: chunk.Usage != nil,
	}, nil
}
