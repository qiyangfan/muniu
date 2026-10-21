// OpenAI 兼容基座供兼容 OpenAI API 的供应商复用；各家差异由 quirk 补丁处理。
package adapter

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/solvela/muniu/internal/failover"
	"github.com/solvela/muniu/internal/protocol"
)

// OpenAI 是 OpenAI 兼容适配器，保存 provider 的 API 基址。
type OpenAI struct {
	baseURL string
}

// NewOpenAI 创建 OpenAI 兼容适配器。
func NewOpenAI(baseURL, apiKey string) *OpenAI {
	return &OpenAI{baseURL: baseURL}
}

// Prepare 构造 /chat/completions 请求，并改写 model 字段。
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

// Classify 按 HTTP 状态码归类上游错误。
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

// openAIUsage 是 OpenAI 响应中的 usage 形态。
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

// NormalizeUsage 将 OpenAI usage 转为统一形态；缺失字段按 0 处理。
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

// PrepareEmbeddings 构造 /embeddings 请求，并改写 model 字段。
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

// RewriteResponse 原样返回响应体，并提取 usage。
func (o *OpenAI) RewriteResponse(body []byte) ([]byte, json.RawMessage, error) {
	var resp struct {
		Usage json.RawMessage `json:"usage"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, nil, fmt.Errorf("adapter: 解析非流式响应: %w", err)
	}
	return body, resp.Usage, nil
}

// StreamEvent 不改写事件内容，passthrough 恒为原字节，meta 供首 token 判定与计量。
func (o *OpenAI) StreamEvent(data []byte) ([]byte, EventMeta, error) {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "[DONE]" {
		return data, EventMeta{IsDone: true}, nil
	}
	if trimmed == "" || strings.HasPrefix(trimmed, ":") {
		// 空 data 或纯注释事件是 SSE 心跳，对客户端不可见也不报错；若按解析失败处理，
		// 合法心跳会被误判为上游断流，长流在心跳点被随机切断
		return nil, EventMeta{}, nil
	}
	chunk, err := protocol.ParseStreamChunk(data)
	if err != nil {
		return nil, EventMeta{}, fmt.Errorf("adapter: 解析 SSE 帧: %w", err)
	}
	return data, EventMeta{
		HasContent: chunk.HasContent(),
		// 帧携带 usage 即视为 usage 帧；Doubao 等供应商挂在 choices 非空的 finish 帧上，
		// choices 为空的纯 usage 终帧同样命中
		IsUsage: chunk.Usage != nil,
	}, nil
}

// ---- 原生协议直通能力 ----

// NativeProto 返回 OpenAI Responses；native_responses 声明的门控由 proxy 判定。
func (o *OpenAI) NativeProto() protocol.InboundProtocol { return protocol.InboundResponses }

// PrepareNative 构造 <base>/responses 直通出站请求，仅改写 model 字段并注入 Bearer；
// native_responses 门控由 provider 配置决定，本方法只校验入站协议匹配。
func (o *OpenAI) PrepareNative(in *protocol.NativeRequest, upstreamModel, apiKey string) (*Outbound, error) {
	if in.Proto != protocol.InboundResponses {
		return nil, fmt.Errorf("adapter: 基座直通仅服务 responses 入站,得到 %q", in.Proto)
	}
	body, err := protocol.RewriteModelInPlace(in.Raw, upstreamModel)
	if err != nil {
		return nil, fmt.Errorf("adapter: 直通 model 改写: %w", err)
	}
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("Authorization", "Bearer "+apiKey)
	return &Outbound{URL: strings.TrimRight(o.baseURL, "/") + "/responses", Header: h, Body: body}, nil
}

// responsesStreamEvent 是 Responses SSE 事件的最小解析形态，未知事件类型前向兼容、对计量不可见。
type responsesStreamEvent struct {
	Type     string `json:"type"`
	Delta    string `json:"delta"`
	ItemID   string `json:"item_id"`
	Response struct {
		ID    string          `json:"id"`
		Usage json.RawMessage `json:"usage"`
	} `json:"response"`
}

// NativeStreamEvent 解析 Responses SSE 事件产出内部 chunk 供计量使用，客户端恒收上游原字节。
// delta 产出内容 chunk；completed 与 incomplete 终帧携带 finish_reason 与原生 usage；failed 报错。
func (o *OpenAI) NativeStreamEvent(data []byte) ([]byte, EventMeta, error) {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || strings.HasPrefix(trimmed, ":") {
		return nil, EventMeta{}, nil // 心跳/注释帧，不可见
	}
	var ev responsesStreamEvent
	if err := json.Unmarshal(data, &ev); err != nil {
		return nil, EventMeta{}, fmt.Errorf("adapter: 解析 responses 事件: %w", err)
	}
	switch ev.Type {
	case "response.output_text.delta":
		if ev.Delta == "" {
			return nil, EventMeta{}, nil
		}
		return chunkBytes2(`{"choices":[{"index":0,"delta":{"content":` + mustJSON2(ev.Delta) + `}}]}`), EventMeta{HasContent: true}, nil
	case "response.function_call_arguments.delta":
		if ev.Delta == "" {
			return nil, EventMeta{}, nil
		}
		// 参数增量按 item_id 透传进内部 chunk，计量仅累计字节数，不消费 index
		return chunkBytes2(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":` + mustJSON2(ev.Delta) + `}}]}}]}`), EventMeta{HasContent: true}, nil
	case "response.completed":
		b := `{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]`
		if len(ev.Response.Usage) > 0 && string(ev.Response.Usage) != "null" {
			b += `,"usage":` + string(ev.Response.Usage)
		}
		return chunkBytes2(b + `}`), EventMeta{IsUsage: len(ev.Response.Usage) > 0 && string(ev.Response.Usage) != "null", IsDone: true}, nil
	case "response.incomplete":
		b := `{"choices":[{"index":0,"delta":{},"finish_reason":"length"}]`
		if len(ev.Response.Usage) > 0 && string(ev.Response.Usage) != "null" {
			b += `,"usage":` + string(ev.Response.Usage)
		}
		return chunkBytes2(b + `}`), EventMeta{IsUsage: len(ev.Response.Usage) > 0 && string(ev.Response.Usage) != "null", IsDone: true}, nil
	case "response.failed", "error":
		return nil, EventMeta{}, fmt.Errorf("adapter: responses 流内失败事件 %q", ev.Type)
	default:
		// created/in_progress/output_item.*/content_part.* 等事件对计量不可见，前向兼容
		return nil, EventMeta{}, nil
	}
}

// responsesUsageForm 是 Responses usage 的原生形态。
type responsesUsageForm struct {
	InputTokens        int64 `json:"input_tokens"`
	OutputTokens       int64 `json:"output_tokens"`
	InputTokensDetails struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"input_tokens_details"`
	OutputTokensDetails struct {
		ReasoningTokens int64 `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
}

// NormalizeNativeUsage 将 Responses usage 转为统一形态，与 chat 路径计量口径一致：
// reasoning 是 output 的子集，只计入细节，不与 output 相加。
func (o *OpenAI) NormalizeNativeUsage(raw json.RawMessage) (protocol.Usage, error) {
	var f responsesUsageForm
	if err := json.Unmarshal(raw, &f); err != nil {
		return protocol.Usage{}, fmt.Errorf("adapter: 解析 responses usage: %w", err)
	}
	return protocol.Usage{
		PromptTokens:     f.InputTokens,
		CompletionTokens: f.OutputTokens,
		CachedTokens:     f.InputTokensDetails.CachedTokens,
		ReasoningTokens:  f.OutputTokensDetails.ReasoningTokens,
	}, nil
}

// PrepareNativeFetch 构造 <base>/responses/{id} 的读删代理请求，无请求体，Bearer 鉴权。
// verb 为 HTTP 动词字面量（GET|DELETE），经 http.NewRequest 透传，非这两种动词由调用方拒绝。
func (o *OpenAI) PrepareNativeFetch(verb, respID, apiKey string) (*Outbound, error) {
	h := http.Header{}
	h.Set("Authorization", "Bearer "+apiKey)
	return &Outbound{
		URL:    strings.TrimRight(o.baseURL, "/") + "/responses/" + url.PathEscape(respID),
		Header: h,
	}, nil
}

// chunkBytes2 与 mustJSON2 为极小封装，与 bridge 包各自持有一份以避免引依赖。
func chunkBytes2(payload string) []byte {
	if payload == "" {
		return nil
	}
	return []byte(payload)
}

func mustJSON2(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(b)
}

// PrepareImages 原位改写模型，其余参数与响应形态由调用方和上游决定。
func (o *OpenAI) PrepareImages(in *protocol.ImageRequest, upstreamModel, apiKey string) (*Outbound, error) {
	body, err := protocol.RewriteModel(in.Raw, upstreamModel)
	if err != nil {
		return nil, err
	}
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("Authorization", "Bearer "+apiKey)
	return &Outbound{URL: strings.TrimRight(o.baseURL, "/") + "/images/generations", Header: h, Body: body}, nil
}
