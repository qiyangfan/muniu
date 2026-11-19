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

// probeSpec 各适配器族的探针端点与鉴权形态;gemini 原生 v1 不支持(登记限制:
// 原生 API 形态不同,免费/推理探针均不适用,该族渠道静默跳过探针、被动统计照常)。
// freePath 为免费控制面端点(证网络/TLS/鉴权/服务在线),inferencePath 为最小推理端点
// (证推理面;免费端点 200 不证推理面——厂商状态页按模型分 lane 统计是直接证据)。
type probeSpec struct {
	freePath      string
	inferencePath string
	anthropic     bool // true = 三态鉴权头(缺省 x-api-key/bearer/oauth,与数据面 bridge.applyAuth 同语义);false = Bearer
}

// adaptersWithProbe 支持探针的适配器族:openai 兼容系共享 /models 与 /chat/completions 形态。
var adaptersWithProbe = map[string]probeSpec{
	"openai":    {freePath: "/models", inferencePath: "/chat/completions"},
	"doubao":    {freePath: "/models", inferencePath: "/chat/completions"},
	"qwen":      {freePath: "/models", inferencePath: "/chat/completions"},
	"deepseek":  {freePath: "/models", inferencePath: "/chat/completions"},
	"moonshot":  {freePath: "/models", inferencePath: "/chat/completions"},
	"glm":       {freePath: "/models", inferencePath: "/chat/completions"},
	"anthropic": {freePath: "/v1/models", inferencePath: "/v1/messages", anthropic: true},
}

// ProbeSupported 适配器是否具备探针能力(装配期据此决定渠道免费探针是否生效)。
func ProbeSupported(adapter string) bool {
	_, ok := adaptersWithProbe[adapter]
	return ok
}

// probeSend 单次探针请求:2xx → OK,429 → Limited(存活但受限),其余 → Fail(带归类)。
// 探针零触碰 key 池:取值来自装配期快照,绝不调用 Pick/Report(EMA/冷却/禁用零写入)。
func (m *Manager) probeSend(ctx context.Context, ch Channel, method, path string, body []byte) (ProbeOutcome, string) {
	key := firstKeyValue(ch.Keys)
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

// firstKeyValue 取首个非空 key(装配期快照,零触碰 key 池)。
func firstKeyValue(keys []ChannelKey) string {
	for _, k := range keys {
		if k.Value != "" {
			return k.Value
		}
	}
	return ""
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

// setProbeAuth 鉴权头形态:anthropic 三态,其余族一律 Bearer。
func setProbeAuth(h http.Header, spec probeSpec, auth, key string) {
	if !spec.anthropic {
		h.Set("Authorization", "Bearer "+key)
		return
	}
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
	payload := map[string]any{
		"model":      ch.ProbeModel,
		"max_tokens": 1,
		"messages":   []map[string]string{{"role": "user", "content": "ping"}},
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ProbeFail, "network"
	}
	return m.probeSend(ctx, ch, http.MethodPost, adaptersWithProbe[ch.Adapter].inferencePath, encoded)
}
