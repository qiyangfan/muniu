// openai 兼容基座:覆盖所有「OpenAI 兼容端点」供应商(DeepSeek/Moonshot/GLM/Qwen/Doubao 等)。
// 差异只进 quirks patch,禁止为兼容家复制整套适配器(ADR-0003)。
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

// ---- 同构直通能力(adapter.NativePreparer/NativeFetcher;protocol-native-passthrough) ----

// NativeProto 基座原生协议 = OpenAI Responses(声明制 native_responses 门控在 proxy 判定)。
func (o *OpenAI) NativeProto() protocol.InboundProtocol { return protocol.InboundResponses }

// PrepareNative Responses 原始体直通出站:<base>/responses,仅 model 原位手术 + Bearer 注入。
// 声明制(native_responses)由 provider 配置门控,本方法只对入站标签做能力边界校验。
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

// responsesStreamEvent Responses SSE 事件读侧形态(公开契约最小集;未知事件类型前向兼容不可见)。
type responsesStreamEvent struct {
	Type     string `json:"type"`
	Delta    string `json:"delta"`
	ItemID   string `json:"item_id"`
	Response struct {
		ID    string          `json:"id"`
		Usage json.RawMessage `json:"usage"`
	} `json:"response"`
}

// NativeStreamEvent Responses SSE meta-only 读侧:delta 事件产出内部统一 chunk(计量
// 拼包与翻译路径同缝),completed 终帧携带 finish_reason 与原生 usage(经
// NormalizeNativeUsage 归一);failed 诚实报错。客户端恒收上游原字节。
func (o *OpenAI) NativeStreamEvent(data []byte) ([]byte, EventMeta, error) {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || strings.HasPrefix(trimmed, ":") {
		return nil, EventMeta{}, nil // 心跳/注释:不可见
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
		// 参数增量按 item_id 透传进内部 chunk 的 index 语义:计量仅累计字节数,不消费 index
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
		// created/in_progress/output_item.*/content_part.* 等:对计量不可见,前向兼容
		return nil, EventMeta{}, nil
	}
}

// responsesUsageForm Responses usage 原生形态(归一表的 responses 列)。
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

// NormalizeNativeUsage Responses usage → 统一 Usage(与 chat 路径计量口径一致:
// reasoning 为 output 子集只入细节,不与 output 相加)。
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

// PrepareNativeFetch 状态 lane 读删代理出站:<base>/responses/{id},无请求体,Bearer。
// verb 为 HTTP 动词字面量(GET|DELETE),经 http.NewRequest 透传,非 GET/DELETE 由调用方拒绝。
func (o *OpenAI) PrepareNativeFetch(verb, respID, apiKey string) (*Outbound, error) {
	h := http.Header{}
	h.Set("Authorization", "Bearer "+apiKey)
	return &Outbound{
		URL:    strings.TrimRight(o.baseURL, "/") + "/responses/" + url.PathEscape(respID),
		Header: h,
	}, nil
}

// chunkBytes2 / mustJSON2:与 bridge 包同款极小封装(包内独立,避免引依赖)。
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
