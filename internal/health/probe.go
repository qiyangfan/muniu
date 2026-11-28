package health

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/solvela/muniu/internal/adapter"
)

// probeSpec 各适配器族的探针端点与鉴权形态;freePath 为免费控制面端点
// (证网络/TLS/鉴权/服务在线),inferencePath 为空表示该族不支持推理探针
// (证推理面;免费端点 200 不证推理面——厂商状态页按模型分 lane 统计是直接证据)。
type probeSpec struct {
	freePath      string
	inferencePath string
	auth          probeAuth
}

// probeAuth 探针鉴权形态:显式枚举而非布尔叠加(gemini 是需要 x-goog-api-key 的第三个特例)。
type probeAuth uint8

const (
	probeAuthBearer    probeAuth = iota // Bearer 头(OpenAI 兼容族缺省)
	probeAuthAnthropic                  // 三态鉴权头(缺省 x-api-key/bearer/oauth,与数据面 bridge.applyAuth 同语义)
	probeAuthGemini                     // x-goog-api-key(Gemini 原生 v1beta)
)

// adaptersWithProbe 支持探针的适配器族:openai 兼容系共享 /models 与 /chat/completions 形态;
// gemini 原生具备免费 models.list,推理探针暂不实现(inferencePath 为空)。
var adaptersWithProbe = map[string]probeSpec{
	"openai":    {freePath: "/models", inferencePath: "/chat/completions"},
	"doubao":    {freePath: "/models", inferencePath: "/chat/completions"},
	"qwen":      {freePath: "/models", inferencePath: "/chat/completions"},
	"deepseek":  {freePath: "/models", inferencePath: "/chat/completions"},
	"moonshot":  {freePath: "/models", inferencePath: "/chat/completions"},
	"glm":       {freePath: "/models", inferencePath: "/chat/completions"},
	"anthropic": {freePath: "/v1/models", inferencePath: "/v1/messages", auth: probeAuthAnthropic},
	"gemini":    {freePath: "/models", auth: probeAuthGemini},
}

// ProbeSupported 适配器是否具备免费控制面探针能力(装配期据此决定渠道免费探针是否生效)。
func ProbeSupported(adapter string) bool {
	_, ok := adaptersWithProbe[adapter]
	return ok
}

// InferenceProbeSupported 适配器是否具备推理探针能力(probe_model 声明与 tick 的执行闸)。
func InferenceProbeSupported(adapter string) bool {
	spec, ok := adaptersWithProbe[adapter]
	return ok && spec.inferencePath != ""
}

// probeSend 单次探针请求:2xx → OK,429 → Limited(存活但受限),其余 → Fail(带归类)。
// 探针零触碰 key 池:取值来自装配期快照,绝不调用 Pick/Report(EMA/冷却/禁用零写入)。
func (m *Manager) probeSend(ctx context.Context, ch Channel, method, path string, body []byte) (ProbeOutcome, string) {
	key := m.nextProbeKey(ch.Name, ch.Keys)
	if key == "" {
		return ProbeFail, "no_key"
	}
	spec, ok := adaptersWithProbe[ch.Adapter]
	if !ok {
		return ProbeFail, "adapter_probe_unsupported"
	}
	req, cancel, err := m.newProbeRequest(ctx, ch, spec, method, path, body, key)
	if err != nil {
		return ProbeFail, "network"
	}
	// 取消随本函数退出:提前在构造处 cancel 会把在途请求掐成 context canceled
	defer cancel()
	resp, err := probeClient(m.client, ch).Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return ProbeFail, "timeout"
		}
		return ProbeFail, "network"
	}
	defer func() { _ = resp.Body.Close() }()
	return probeOutcomeOf(resp.StatusCode)
}

// nextProbeKey 按渠道轮换取下一个可用 key;空列表返回空串(调用方跳过探针)。
func (m *Manager) nextProbeKey(name string, keys []ChannelKey) string {
	if len(keys) == 0 {
		return ""
	}
	m.mu.Lock()
	i := m.probeIdx[name] % len(keys)
	m.probeIdx[name] = (i + 1) % len(keys)
	m.mu.Unlock()
	k := keys[i]
	if k.Value == "" {
		return ""
	}
	return k.Value
}

// probeClient 渠道声明出站代理 client(装配期按 proxy_url 解析),优先于默认。
func probeClient(def *http.Client, ch Channel) *http.Client {
	if ch.Outbound != nil {
		return ch.Outbound
	}
	return def
}

// newProbeRequest 组装探针请求(超时经 ctx 注入,与评估循环同口径);
// 返回的 cancel 由调用方在请求结束后调用。
func (m *Manager) newProbeRequest(ctx context.Context, ch Channel, spec probeSpec, method, path string,
	body []byte, key string) (*http.Request, context.CancelFunc, error) {
	// 尾斜杠归一化与 adapter 层同口径:数据面容忍的 base_url 写法不得在探针面拼出 //path 404
	url := strings.TrimRight(ch.BaseURL, "/") + path
	ctx, cancel := context.WithTimeout(ctx, m.opts.Timeout)
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		cancel()
		return nil, nil, err
	}
	setProbeAuth(req.Header, spec, ch.Auth, key)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, cancel, nil
}

// setProbeAuth 鉴权头形态:gemini 用 x-goog-api-key,anthropic 三态,其余族 Bearer。
func setProbeAuth(h http.Header, spec probeSpec, auth, key string) {
	switch spec.auth {
	case probeAuthGemini:
		h.Set("x-goog-api-key", key)
	case probeAuthAnthropic:
		// 三态与数据面 bridge.applyAuth 同语义(oauth-subscription-lane):oauth 渠道若缺
		// 此分支,缺省全开的免费探针会以 x-api-key 发订阅 access token → 连续 401 → 健康面
		// 误熔断可用渠道,且畸形凭据外呼构成保号风险(评审域三 P1)。
		switch auth {
		case adapter.AuthOAuth:
			h.Set("Authorization", "Bearer "+key)
			h.Set("anthropic-beta", adapter.AuthOAuthBetaHeader)
		case adapter.AuthBearer:
			h.Set("Authorization", "Bearer "+key)
		default:
			h.Set("x-api-key", key)
		}
		h.Set("anthropic-version", "2023-06-01")
	default:
		h.Set("Authorization", "Bearer "+key)
	}
}

// probeOutcomeOf 状态码 → 探针结论:2xx OK,429 受限,其余失败(带归类)。
func probeOutcomeOf(status int) (ProbeOutcome, string) {
	switch {
	case status >= 200 && status < 300:
		return ProbeOK, ""
	case status == http.StatusTooManyRequests:
		return ProbeLimited, "http_status_429"
	default:
		return ProbeFail, fmt.Sprintf("http_status_%d", status)
	}
}

// freeProbe 免费控制面探测。
func (m *Manager) freeProbe(ctx context.Context, ch Channel) (ProbeOutcome, string) {
	return m.probeSend(ctx, ch, http.MethodGet, adaptersWithProbe[ch.Adapter].freePath, nil)
}

// inferenceProbe 付费推理面探测:最小补全请求(max_tokens 微值,零输出成本量级)。
func (m *Manager) inferenceProbe(ctx context.Context, ch Channel) (ProbeOutcome, string) {
	spec, ok := adaptersWithProbe[ch.Adapter]
	if !ok || spec.inferencePath == "" {
		return ProbeFail, "adapter_inference_probe_unsupported"
	}
	payload := map[string]any{
		"model":      ch.ProbeModel,
		"max_tokens": 1,
		"messages":   []map[string]string{{"role": "user", "content": "ping"}},
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ProbeFail, "network"
	}
	return m.probeSend(ctx, ch, http.MethodPost, spec.inferencePath, encoded)
}
