// Package adapter 把入站 OpenAI 形态请求翻译为上游调用,并把上游响应/流归一化回 OpenAI 形态。
// 分类学与基座/quirk/bridge 三分法见 ADR-0003 与 upstream-adapters.md。
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

// Adapter 五方法契约(upstream-adapters「Adapter 接口」,方法集为契约)。
type Adapter interface {
	// Prepare 改写出站请求:URL 拼接、鉴权头注入(key 由 keypool 每请求提供)、
	// model 改写(经 protocol.RewriteModel,未知字段零丢失)。
	Prepare(in *protocol.ChatRequest, upstreamModel, apiKey string) (*Outbound, error)
	// Classify 把上游错误响应归类为 failover 判定表所需的 ErrorClass。
	Classify(status int, body []byte) failover.ErrorClass
	// NormalizeUsage 上游 usage 原始 JSON → 统一 Usage(归一表见 upstream-adapters)。
	NormalizeUsage(raw json.RawMessage) (protocol.Usage, error)
	// StreamEvent 单事件读侧归一(计量与首 token 判定用);passthrough 返回原字节
	// (字段改写白名单:兼容型零改写;桥接型翻译为 OpenAI 形态——唯一改写者)。
	StreamEvent(data []byte) (passthrough []byte, meta EventMeta, err error)
	// RewriteResponse 非流式响应改写:基座原样透传并抽取 usage;桥接型翻译为 OpenAI 形态。
	// upstream-adapters 五方法之外的新增方法:桥接非流式的客观需要,已登记 HANDOFF。
	RewriteResponse(body []byte) (out []byte, usage json.RawMessage, err error)
}

// Outbound 出站请求形态(transport 层唯一消费物)。
type Outbound struct {
	URL    string
	Header http.Header
	Body   []byte
}

// EventMeta 单事件的读侧元信息(首 token 判定与计量消费);JSON 标签供 conformance fixture 对齐。
type EventMeta struct {
	HasContent bool `json:"has_content"` // 实质内容帧(content/reasoning_content/tool_call 任一非空)——首 token 判定
	IsUsage    bool `json:"is_usage"`    // usage 终帧
	IsDone     bool `json:"is_done"`     // [DONE] 终止标记
}

// For 按适配器类型构造实例(config 的 adapter 字段=类型,provider 实例名可任意取):
// openai 为兼容基座;deepseek/moonshot/glm = 基座 + 各家 quirk(差异只声明差异,ADR-0003);
// qwen/doubao 当前与基座无已实证差异;anthropic/gemini 桥接属后续阶段,未实现即报错(fail-fast)。
func For(adapterType, baseURL, apiKey string) (Adapter, error) {
	base := NewOpenAI(baseURL, apiKey)
	switch adapterType {
	case "openai", "qwen", "doubao":
		return base, nil
	case "deepseek":
		return newDeepseek(base), nil
	case "moonshot":
		return newMoonshot(base), nil
	case "glm":
		return newGLM(base), nil
	case "anthropic":
		// 桥接工厂由 bridge 包 init 注册(避免 adapter↔bridge 循环依赖)
		if anthropicFactory != nil {
			return anthropicFactory(baseURL, apiKey), nil
		}
		return nil, fmt.Errorf("adapter: anthropic 桥接未接线(需导入 adapter/bridge)")
	default:
		return nil, fmt.Errorf("adapter: 适配器类型 %q 未实现或未知", adapterType)
	}
}

// anthropicFactory 桥接构造工厂(bridge 包经 SetAnthropicFactory 注册)。
var anthropicFactory func(baseURL, apiKey string) Adapter

// SetAnthropicFactory 注册桥接工厂(bridge 包 init 调用;应用方需 blank import bridge)。
func SetAnthropicFactory(f func(baseURL, apiKey string) Adapter) {
	anthropicFactory = f
}

// EmbeddingPreparer embeddings 出站能力(可选能力接口:基座支持,桥接型按需实现)。
// proxy 经类型断言探测;不支持的适配器 → 入站请求归 bad_request。
type EmbeddingPreparer interface {
	PrepareEmbeddings(in *protocol.EmbeddingRequest, upstreamModel, apiKey string) (*Outbound, error)
}

// Registry provider 名 → Adapter 实例的进程内注册表(装配期构建,运行期只读)。
type Registry struct {
	mu       sync.RWMutex
	adapters map[string]Adapter
}

// NewRegistry 空注册表。
func NewRegistry() *Registry {
	return &Registry{adapters: map[string]Adapter{}}
}

// Register 注册/覆盖一个 provider 的适配器(装配期调用)。
func (r *Registry) Register(provider string, a Adapter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.adapters[provider] = a
}

// Get 按 provider 名取适配器。
func (r *Registry) Get(provider string) (Adapter, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.adapters[provider]
	return a, ok
}

// Names 返回已注册 provider 名(排序,便于日志与自检)。
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
