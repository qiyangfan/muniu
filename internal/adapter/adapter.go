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

// 鉴权形态声明(ADR-0010 同哲学:形态来自显式声明,零自动探测)。
// 协议事实:官方 Anthropic Messages API 凭证走 x-api-key(Bearer 保留给 OAuth,
// 对其发 Bearer 形态 API key 即 401);国内分支官方文档普遍以 AUTH_TOKEN(Bearer)
// 演示。两者都对,按上游声明选择。
const (
	AuthDefault = ""       // 缺省:anthropic=x-api-key;openai 系=Bearer(基座固有)
	AuthBearer  = "bearer" // anthropic 桥接改发 Authorization: Bearer <key>(国内分支口径)
)

// Options 适配器构造选项(零值=协议缺省);经 config.Provider.Auth 声明传入。
type Options struct {
	Auth string
}

// Types 返回 For 可构造的适配器类型集合(排序稳定),即配置面 providers[].adapter 的值域单源:
// internal/config 的校验值域与非法取值报错均由本函数渲染,不再手抄清单(ADR-0003 执法面)。
// 新增 For 分支必须同步入列——types_test.go 断言每个列出的值都能被 For 真实构造。
func Types() []string {
	return []string{"anthropic", "deepseek", "doubao", "gemini", "glm", "moonshot", "openai", "qwen"}
}

// For 按适配器类型构造实例(config 的 adapter 字段=类型,provider 实例名可任意取):
// openai 为兼容基座;deepseek/moonshot/glm = 基座 + 各家 quirk(差异只声明差异,ADR-0003);
// qwen/doubao 当前与基座无已实证差异;anthropic 走桥接工厂(需 blank import bridge);
// gemini 为原生整家实现(gemini-native-protocol)。
// opts 仅 anthropic 消费(鉴权形态声明),其余类型忽略。
func For(adapterType, baseURL, apiKey string, opts ...Options) (Adapter, error) {
	base := NewOpenAI(baseURL, apiKey)
	var opt Options
	if len(opts) > 0 {
		opt = opts[0]
	}
	switch adapterType {
	case "openai", "qwen", "doubao":
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
		// 桥接工厂由 bridge 包 init 注册(避免 adapter↔bridge 循环依赖)
		if anthropicFactory != nil {
			return anthropicFactory(baseURL, opt), nil
		}
		return nil, fmt.Errorf("adapter: anthropic 桥接未接线(需导入 adapter/bridge)")
	default:
		return nil, fmt.Errorf("adapter: 适配器类型 %q 未实现或未知", adapterType)
	}
}

// anthropicFactory 桥接构造工厂(bridge 包经 SetAnthropicFactory 注册)。
var anthropicFactory func(baseURL string, opts Options) Adapter

// SetAnthropicFactory 注册桥接工厂(bridge 包 init 调用;应用方需 blank import bridge)。
func SetAnthropicFactory(f func(baseURL string, opts Options) Adapter) {
	anthropicFactory = f
}

// EmbeddingPreparer embeddings 出站能力(可选能力接口:基座支持,桥接型按需实现)。
// proxy 经类型断言探测;不支持的适配器 → 入站请求归 bad_request。
type EmbeddingPreparer interface {
	PrepareEmbeddings(in *protocol.EmbeddingRequest, upstreamModel, apiKey string) (*Outbound, error)
}

// NativePreparer 同构直通出站能力(可选能力接口,与 EmbeddingPreparer 同款,proxy 类型断言探测)。
// 实现者声明「具备原生协议出站通道」:messages × anthropic 桥接、responses × openai 基座
// (后者由 provider 配置 native_responses 声明另行门控,声明制见 protocol-native-passthrough)。
// 契约:出站 body 仅 model 字段原位手术(RewriteModelInPlace),其余逐字节原样——
// 直通不属「字段改写白名单」约束域(零改写,upstream-adapters 注记)。
type NativePreparer interface {
	// NativeProto 本适配器原生直通的入站协议(协议身份自述;判定 = 能力 ∧ 身份匹配:
	// 桥接=messages,基座=responses;同名能力不得跨协议误判)。
	NativeProto() protocol.InboundProtocol
	// PrepareNative 原始体直通出站:URL/鉴权头注入按适配器既有声明。
	PrepareNative(in *protocol.NativeRequest, upstreamModel, apiKey string) (*Outbound, error)
	// NativeStreamEvent 直通流事件 meta-only 读侧:返回内部统一 chunk(计量/缓存拼包
	// 与翻译路径同缝消费,可为 nil)+ EventMeta;客户端恒收上游原字节,本方法不改写 wire。
	NativeStreamEvent(data []byte) (internal []byte, meta EventMeta, err error)
	// NormalizeNativeUsage 直通响应的原生 usage → 统一 Usage(计量口径与翻译路径一致)。
	NormalizeNativeUsage(raw json.RawMessage) (protocol.Usage, error)
}

// NativeFetcher 直通 GET 代理能力(responses 有状态查询:亲和已知时由 proxy 调用)。
type NativeFetcher interface {
	// PrepareNativeFetch 状态查询出站:GET <原生端点>/{id},无请求体,鉴权按既有声明。
	PrepareNativeFetch(respID, apiKey string) (*Outbound, error)
}

// ToolRoundtripCompleter 工具多轮翻译完整性声明(gemini-native-protocol 决策 4,声明式缺口):
// 桥接型适配器如实声明工具多轮「请求侧」翻译是否完整。豁免判定 = Bridger ∧ 未声明完整——
// anthropic 桥接声明 false(现状豁免行为逐字节不变),gemini 声明 true(工具多轮正常出站
// 不豁免)。未实现本接口的桥接型按「不完整」处理(保守语义)。
type ToolRoundtripCompleter interface {
	ToolRoundtripComplete() bool
}

// StreamCloseDone 无流终止标记协议声明:干净关流即正常收尾(等价 IsDone)。
// Gemini alt=sse 无 [DONE];未实现者维持「断流=mid_stream_fail」既有契约。
type StreamCloseDone interface {
	StreamCloseIsDone() bool
}

// StreamSequencer 每流事件测序能力(可选,与 StreamCloseDone 同族):wire 本身不携带
// 工具调用序号的协议(Gemini 并行 functionCall 无 index 字段),跨帧并行调用必须按流
// 计数才能合成正确的 tool_calls index/id——无状态 StreamEvent 对同帧多调用只能帧内计数。
// 实现者返回每流事件函数(单请求单实例,非并发安全);proxy 管道优先消费,
// 未实现者维持无状态 StreamEvent 既有行为。
type StreamSequencer interface {
	NewStreamSequencer() func(data []byte) (passthrough []byte, meta EventMeta, err error)
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

// Bridger 桥接型适配器标记(出站协议翻译,非 OpenAI 原生形态;基座/quirk 不实现)。
// proxy 据此对「工具多轮中途流量」做适配性豁免判定(upstream-egress-batch 契约):
// 桥接请求侧 role=tool/tool_calls 翻译未实现,此类流量对该候选不可服务。
// 断言接口实现,无登记表——新增桥接型适配器时实现本接口即自动纳入判定。
type Bridger interface {
	BridgeKind() string
}
