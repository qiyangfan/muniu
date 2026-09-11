// 语义缓存拦截(T4.5 集成):命中回放省上游,未命中照常出站并在成功后按准入入库。
// 准入(semantic-cache.md):temperature≤0.3 · finish_reason=stop · 无工具调用 · 输入 ≤8K tokens · 租户开关。
package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/solvela/muniu/internal/auth"
	"github.com/solvela/muniu/internal/failover"
	"github.com/solvela/muniu/internal/metering"
	"github.com/solvela/muniu/internal/protocol"
	"github.com/solvela/muniu/internal/semantic"
)

// cachePhase 单请求缓存状态(句柄式:lookup 命中→回放;否则出站成功后 store)。
type cachePhase struct {
	enabled bool
	fp      string
	vec     []float32
	inputTk int64
}

// lookupPhase 检索阶段:准入判定→嵌入→两级检索。任何失败静默降级为不命中(缓存故障不得影响请求)。
func (p *Proxy) lookupCache(r *http.Request, req *protocol.ChatRequest) (*semantic.LookupResult, *cachePhase) {
	if p.cache == nil {
		return nil, nil
	}
	defer func() { _ = recover() }() // 缓存路径任何 panic 一律吞掉降级(宁缺勿错的工程化表达)
	info := auth.KeyFrom(r.Context())
	if info == nil || !p.cacheTenantEnabled(info.TenantID) {
		return nil, nil
	}
	// 带工具的请求不参与缓存:二级语义检索按 messages 向量全局 TopK,无法感知 tools 差异,
	// 不同工具定义的同义请求会错拿彼此答案(宁缺勿错;键级 tools 指纹拦不住向量检索)
	if len(req.Tools) > 0 && string(req.Tools) != "null" {
		return nil, nil
	}
	if !semantic.TemperatureAdmissible(req.Temperature) {
		return nil, nil
	}
	inputText, err := json.Marshal(req.Messages)
	if err != nil {
		return nil, nil
	}
	inputTokens, _ := metering.CountTokens(string(inputText), req.Model)
	if inputTokens > 8000 {
		return nil, nil
	}
	// tools 指纹:请求级 tools 已整体排除出缓存,此处恒空串(Fingerprint 形参保留给未来细粒度准入)
	vec, err := p.cache.EmbedInput(r.Context(), string(inputText))
	if err != nil {
		p.logger.Warn("语义缓存嵌入失败,跳过检索", "request_id", loggingRequestID(r.Context()), "error_detail", err.Error())
		return nil, nil
	}
	fp := semantic.Fingerprint(req.Model, string(inputText), "")
	res, hit := p.cache.Lookup(r.Context(), info.TenantID, fp, vec)
	if hit {
		return res, &cachePhase{enabled: true, fp: fp, vec: vec, inputTk: inputTokens}
	}
	return nil, &cachePhase{enabled: true, fp: fp, vec: vec, inputTk: inputTokens}
}

// storePhase 存储阶段:出站成功且响应准入 → 入库。stream 场景传拼接出的响应体。
func (p *Proxy) storePhase(r *http.Request, ph *cachePhase, req *protocol.ChatRequest, cand failover.Candidate, responseBody []byte) {
	if p.cache == nil || ph == nil || !ph.enabled {
		return
	}
	defer func() { _ = recover() }()
	if !semantic.ResponseAdmissible(responseBody, int(ph.inputTk)) {
		return
	}
	_ = p.cache.Store(r.Context(), authTenant(r.Context()), ph.fp, req.Model, ph.vec, responseBody)
}

// replayNonStream 命中回放(非流式):存储的 OpenAI 响应直接写出。
func (p *Proxy) replayNonStream(w http.ResponseWriter, r *http.Request, res *semantic.LookupResult) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(res.Response)
}

// replayStream 命中回放(流式):按 chunk 拼包回放,finish_reason=stop,补 [DONE] 干净收尾。
func (p *Proxy) replayStream(w http.ResponseWriter, r *http.Request, res *semantic.LookupResult) {
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage json.RawMessage `json:"usage"`
	}
	_ = json.Unmarshal(res.Response, &parsed)
	content := ""
	var usage protocol.Usage
	if len(parsed.Choices) > 0 {
		content = parsed.Choices[0].Message.Content
	}
	if len(parsed.Usage) > 0 {
		_ = json.Unmarshal(parsed.Usage, &usage)
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flush(w)

	frame := func(delta string) {
		_, _ = w.Write([]byte(`data: {"choices":[{"index":0,"delta":{"content":` + mustJSONStr(delta) + `}}]}` + "\n\n"))
		flush(w)
	}
	// 拼包回放:按 64 字节切段模拟流式节奏(纯回放,不再请求上游)
	for _, seg := range splitForReplay(content, 64) {
		frame(seg)
	}
	_, _ = w.Write([]byte(`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n"))
	flush(w)
	_, _ = w.Write([]byte("data: [DONE]\n\n"))
	flush(w)
}

// splitForReplay 回放切段:按 size 字节目标切分,但切点必须落在 rune 边界——
// 按字节硬切会把多字节字符劈成非法 UTF-8,经 json.Marshal 被替换为 U+FFFD,
// 命中回放的中文正文永久乱码。
func splitForReplay(content string, size int) []string {
	if size <= 0 || len(content) <= size {
		if content == "" {
			return nil
		}
		return []string{content}
	}
	var out []string
	for i := 0; i < len(content); {
		end := i + size
		if end >= len(content) {
			end = len(content)
		} else {
			for end > i && !utf8.RuneStart(content[end]) {
				end--
			}
		}
		out = append(out, content[i:end])
		i = end
	}
	return out
}

// recordCacheHit 命中行计量:provider=semantic-cache,cost 0(semantic-cache.md「计量与可观测」)。
// TotalMs 记请求入口到命中判定的真实耗时(含嵌入自调用——命中路径的主要延迟),兑现契约「命中延迟单独字段」。
func (p *Proxy) recordCacheHit(r *http.Request, req *protocol.ChatRequest, res *semantic.LookupResult, hitStart time.Time) {
	if p.pipeline == nil {
		return
	}
	info := auth.KeyFrom(r.Context())
	tenantID, apiKeyID := "", ""
	if info != nil {
		tenantID, apiKeyID = info.TenantID, info.ID
	}
	p.pipeline.Submit(metering.NewRow(metering.RowParams{
		RequestID: loggingRequestID(r.Context()), TenantID: tenantID, APIKeyID: apiKeyID,
		Provider: "semantic-cache", ModelAlias: req.Model, UpstreamModel: "semantic-cache",
		Stream: req.Stream, Status: "ok",
		Usage: usageOf(res), Currency: "CNY", TotalMs: time.Since(hitStart).Milliseconds(),
	}))
}

// usageOf 从回放体解析 usage(缺失按 0)。cached_tokens 恒置 0:
// 命中行未发生上游缓存读,口径必须与 semantic-cache.md「cached_tokens=0」一致,不沿用存量的上游命中数。
func usageOf(res *semantic.LookupResult) (u protocol.Usage) {
	if res == nil {
		return
	}
	var parsed struct {
		Usage *protocol.Usage `json:"usage"`
	}
	if err := json.Unmarshal(res.Response, &parsed); err == nil && parsed.Usage != nil {
		u = *parsed.Usage
		u.CachedTokens = 0
	}
	return
}

// authTenant ctx 取租户 id(命中行归属)。
func authTenant(ctx context.Context) string {
	if info := auth.KeyFrom(ctx); info != nil {
		return info.TenantID
	}
	return ""
}

// mustJSONStr 字符串 → JSON 字串字面量(含转义)。
func mustJSONStr(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
