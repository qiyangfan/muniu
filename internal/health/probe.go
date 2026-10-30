package health

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// probeSpec 各适配器族的探针端点与鉴权形态;gemini 原生 v1 不支持(登记限制:
// 原生 API 形态不同,免费/推理探针均不适用,该族渠道静默跳过探针、被动统计照常)。
// freePath 为免费控制面端点(证网络/TLS/鉴权/服务在线),inferencePath 为最小推理端点
// (证推理面;免费端点 200 不证推理面——厂商状态页按模型分 lane 统计是直接证据)。
type probeSpec struct {
	freePath      string
	inferencePath string
	anthropic     bool // true = x-api-key + anthropic-version 鉴权头;false = Bearer
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
func (m *Manager) probeSend(ch Channel, method, path string, body []byte) (ProbeOutcome, string) {
	key := ""
	for _, k := range ch.Keys {
		if k.Value != "" {
			key = k.Value
			break
		}
	}
	if key == "" {
		return ProbeFail, "no_key"
	}
	spec, ok := adaptersWithProbe[ch.Adapter]
	if !ok {
		return ProbeFail, "adapter_probe_unsupported"
	}
	// 尾斜杠归一化与 adapter 层同口径:数据面容忍的 base_url 写法不得在探针面拼出 //path 404
	url := strings.TrimRight(ch.BaseURL, "/") + path
	ctx, cancel := context.WithTimeout(m.ctx, m.opts.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return ProbeFail, "network"
	}
	if spec.anthropic {
		if ch.Auth == "bearer" {
			req.Header.Set("Authorization", "Bearer "+key)
		} else {
			req.Header.Set("x-api-key", key)
		}
		req.Header.Set("anthropic-version", "2023-06-01")
	} else {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := m.client.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return ProbeFail, "timeout"
		}
		return ProbeFail, "network"
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return ProbeOK, ""
	case resp.StatusCode == http.StatusTooManyRequests:
		return ProbeLimited, "http_status_429"
	default:
		return ProbeFail, fmt.Sprintf("http_status_%d", resp.StatusCode)
	}
}

// freeProbe 免费控制面探测。
func (m *Manager) freeProbe(ch Channel) (ProbeOutcome, string) {
	return m.probeSend(ch, http.MethodGet, adaptersWithProbe[ch.Adapter].freePath, nil)
}

// inferenceProbe 付费推理面探测:最小补全请求(max_tokens 微值,零输出成本量级)。
func (m *Manager) inferenceProbe(ch Channel) (ProbeOutcome, string) {
	payload := map[string]any{
		"model":      ch.ProbeModel,
		"max_tokens": 1,
		"messages":   []map[string]string{{"role": "user", "content": "ping"}},
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ProbeFail, "network"
	}
	return m.probeSend(ch, http.MethodPost, adaptersWithProbe[ch.Adapter].inferencePath, encoded)
}
