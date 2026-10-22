// Package adapter 将规范请求转换为上游协议，并把响应归一化为 OpenAI 形态。
package adapter

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"sync"

	"github.com/solvela/muniu/internal/failover"
	"github.com/solvela/muniu/internal/protocol"
)

// Adapter 转换请求、响应、流事件和 usage。
type Adapter interface {
	// Prepare 构造出站请求并注入鉴权头。
	Prepare(in *protocol.ChatRequest, upstreamModel, apiKey string) (*Outbound, error)
	// Classify 将上游状态归类为 failover 错误。
	Classify(status int, body []byte) failover.ErrorClass
	// NormalizeUsage 将上游 usage 转为统一形态。
	NormalizeUsage(raw json.RawMessage) (protocol.Usage, error)
	// StreamEvent 解析单个流事件；passthrough 是客户端收到的事件内容。
	StreamEvent(data []byte) (passthrough []byte, meta EventMeta, err error)
	// RewriteResponse 将非流式响应转换为 OpenAI 形态，并返回上游 usage。
	RewriteResponse(body []byte) (out []byte, usage json.RawMessage, err error)
}

// Outbound 是适配器产出的上游请求，由 transport 发送。
type Outbound struct {
	URL    string
	Header http.Header
	Body   []byte
}

// EventMeta 保存单个流事件的内容、usage 和终止标记。
type EventMeta struct {
	HasContent bool `json:"has_content"` // content/reasoning_content/tool_call 任一非空，用于首 token 判定
	IsUsage    bool `json:"is_usage"`    // usage 终帧
	IsDone     bool `json:"is_done"`     // [DONE] 终止标记
}

// Anthropic 鉴权方式由 provider 配置显式声明，不自动探测。
// 官方 API key 使用 x-api-key；部分兼容分支使用 Bearer。
const (
	AuthDefault = ""       // 缺省：anthropic 用 x-api-key，openai 系用 Bearer
	AuthBearer  = "bearer" // anthropic 桥接改发 Authorization: Bearer <key>，用于国内兼容分支
)

// Options 保存适配器构造选项；零值表示协议默认值。
type Options struct {
	Auth                         string
	NativeMultimodalEmbeddingURL string
}

// Types 返回可由 For 构造的适配器类型，config 校验与错误提示复用此列表。
func Types() []string {
	return []string{"anthropic", "deepseek", "doubao", "gemini", "glm", "moonshot", "openai", "qwen"}
}

// For 根据适配器类型构造实例。Anthropic 使用桥接工厂，Gemini 使用原生适配器；
// opts 仅用于 Anthropic 鉴权配置。
func For(adapterType, baseURL, apiKey string, opts ...Options) (Adapter, error) {
	base := NewOpenAI(baseURL, apiKey)
	var opt Options
	if len(opts) > 0 {
		opt = opts[0]
	}
	switch adapterType {
	case "openai", "doubao":
		return base, nil
	case "qwen":
		base.nativeMultimodalEmbeddingURL = opt.NativeMultimodalEmbeddingURL
		return base, nil
	case "deepseek":
		return newDeepseek(base), nil
	case "moonshot":
		return newMoonshot(base), nil
	case "glm":
		return newGLM(base), nil
	case "gemini":
		return NewGemini(baseURL), nil
	case "anthropic":
		if opt.Auth != AuthDefault && opt.Auth != AuthBearer {
			return nil, fmt.Errorf("adapter: 不支持的鉴权形态 %q(允许 空|%s)", opt.Auth, AuthBearer)
		}
		// 桥接工厂由 bridge 包 init 注册，以避免 adapter 与 bridge 循环依赖
		if anthropicFactory != nil {
			return anthropicFactory(baseURL, opt), nil
		}
		return nil, fmt.Errorf("adapter: anthropic 桥接未接线(需导入 adapter/bridge)")
	default:
		return nil, fmt.Errorf("adapter: 适配器类型 %q 未实现或未知", adapterType)
	}
}

// anthropicFactory 桥接构造工厂，由 bridge 包经 SetAnthropicFactory 注册。
var anthropicFactory func(baseURL string, opts Options) Adapter

// SetAnthropicFactory 注册 Anthropic 桥接工厂。
func SetAnthropicFactory(f func(baseURL string, opts Options) Adapter) {
	anthropicFactory = f
}

// EmbeddingPreparer 是适配器可选的 embeddings 出站能力。
type EmbeddingPreparer interface {
	PrepareEmbeddings(in *protocol.EmbeddingRequest, upstreamModel, apiKey string) (*Outbound, error)
}

// MultimodalEmbeddingPreparer 是显式配置原生端点时可选的多模态向量出站能力。
type MultimodalEmbeddingPreparer interface {
	SupportsMultimodalEmbeddings(upstreamModel string) bool
	PrepareMultimodalEmbeddings(in *protocol.MultimodalEmbeddingRequest, upstreamModel, apiKey string) (*Outbound, error)
	NormalizeMultimodalEmbeddingUsage(raw json.RawMessage, upstreamModel string) (protocol.Usage, error)
}

// NativePreparer 是适配器可选的原生协议直通能力。
// 直通请求只改写 model 字段，其余请求体和客户端收到的事件保持原样。
type NativePreparer interface {
	// NativeProto 返回适配器支持直通的入站协议。
	NativeProto() protocol.InboundProtocol
	// PrepareNative 构造原生协议的出站请求。
	PrepareNative(in *protocol.NativeRequest, upstreamModel, apiKey string) (*Outbound, error)
	// NativeStreamEvent 解析事件供计量等内部逻辑使用，不改写客户端收到的原始事件。
	NativeStreamEvent(data []byte) (internal []byte, meta EventMeta, err error)
	// NormalizeNativeUsage 将原生协议 usage 转为统一形态。
	NormalizeNativeUsage(raw json.RawMessage) (protocol.Usage, error)
}

// NativeFetcher 是适配器可选的原生状态查询和删除能力。
type NativeFetcher interface {
	// PrepareNativeFetch 构造对原生状态端点的 GET 或 DELETE 请求。
	PrepareNativeFetch(verb, respID, apiKey string) (*Outbound, error)
}

// ToolRoundtripCompleter 声明工具多轮请求翻译是否完整；桥接适配器未实现时按不完整处理。
type ToolRoundtripCompleter interface {
	ToolRoundtripComplete() bool
}

// StreamCloseDone 声明干净关流是否表示正常结束；未实现时关流按中途失败处理。
type StreamCloseDone interface {
	StreamCloseIsDone() bool
}

// StreamSequencer 为单个请求创建事件解析器，用于协议未提供工具调用序号时跨帧编号。
// 返回的解析器不支持并发调用；未实现时使用无状态 StreamEvent。
type StreamSequencer interface {
	NewStreamSequencer() func(data []byte) (passthrough []byte, meta EventMeta, err error)
}

// Registry 是 provider 名到 Adapter 实例的进程内注册表，装配期构建，运行期只读。
type Registry struct {
	mu       sync.RWMutex
	adapters map[string]Adapter
}

// NewRegistry 创建空注册表。
func NewRegistry() *Registry {
	return &Registry{adapters: map[string]Adapter{}}
}

// Register 注册或替换 provider 的适配器。
func (r *Registry) Register(provider string, a Adapter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.adapters[provider] = a
}

// Get 返回指定 provider 的适配器。
func (r *Registry) Get(provider string) (Adapter, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.adapters[provider]
	return a, ok
}

// Names 返回已注册 provider 名，排序便于日志与自检。
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.adapters))
	for n := range r.adapters {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Bridger 标记需要将规范请求翻译为其他协议的适配器。
type Bridger interface {
	BridgeKind() string
}

// ImagePreparer 同步生成是可选能力，桥接适配器不声明即明确拒绝。
type ImagePreparer interface {
	PrepareImages(in *protocol.ImageRequest, upstreamModel, apiKey string) (*Outbound, error)
}
