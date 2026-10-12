// 数据面指标插桩(ADR-0009:零依赖直写;未启用时全部零开销短路)。
package proxy

import (
	"github.com/solvela/muniu/internal/metrics"
	"github.com/solvela/muniu/internal/protocol"
)

var (
	// RequestsTotal 转发请求计数 {model,status}。
	RequestsTotal = metrics.NewCounterVec("muniu_requests_total", "转发请求数", "model", "status")
	// UpstreamTotal 上游结果计数 {provider,class}。
	UpstreamTotal = metrics.NewCounterVec("muniu_upstream_total", "上游结果数(按错误分类)", "provider", "class")
	// RequestDuration 请求总耗时直方图(ms)。
	RequestDuration = metrics.NewHistogram("muniu_request_duration_ms", "请求总耗时(ms)", metrics.DefBuckets)
	// TokensTotal token 流向计数 {direction,provider,model}(gateway-parity「usage-metrics」):
	// 计量落账点累加,口径 = usage_logs 落账行(含估算行);direction ∈ prompt|cached_prompt|completion
	// (reasoning 按计价口径计入 output 段);标签基数受配置界(provider × 别名 × 3 向)。
	TokensTotal = metrics.NewCounterVec("muniu_tokens_total", "token 计数(与 usage_logs 落账行同口径)", "direction", "provider", "model")
	// SemanticCacheQueries 语义缓存判定计数 {model,result}(result ∈ hit|miss|skip:
	// 实际检索命中=hit/未命中=miss/缓存未参与(租户开关关、tools 请求、温度不合规等)=skip)。
	SemanticCacheQueries = metrics.NewCounterVec("muniu_semantic_cache_queries_total", "语义缓存判定数", "model", "result")
	// StickyRequestsTotal 有状态粘性请求数 {result}(原生 responses lane:
	// hit=亲和命中;lost=亲和未知/失效自解释降级;unsupported=别名无原生 lane)
	StickyRequestsTotal = metrics.NewCounterVec("muniu_sticky_requests_total", "有状态粘性请求数", "result")
	// FirstTokenDuration 流式首 token 延迟直方图(ms);桶 1ms 起步——压测实测流式首字节
	// p50≈1.13ms,DefBuckets(5ms 起)会把最感兴趣区间吞进第一个桶。仅流式请求观测。
	FirstTokenDuration = metrics.NewHistogram("muniu_first_token_duration_ms", "流式首 token 延迟(ms)", []float64{1, 2, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000})
)

// observeTokens token 三向累加(record 与 recordCacheHit 两个落账点共用;
// 恒写三个方向使序列在 (provider,alias) 粒度恒定存在,便于率计算)。
// completion 方向 = usage_logs.completion_tokens 同字段(不含 reasoning 列):
// 上游语义中 reasoning_tokens 是 completion 的子集,相加即双计(域B 审查确认)。
func observeTokens(provider, alias string, u protocol.Usage) {
	TokensTotal.Add(u.PromptTokens, "prompt", provider, alias)
	TokensTotal.Add(u.CachedTokens, "cached_prompt", provider, alias)
	TokensTotal.Add(u.CompletionTokens, "completion", provider, alias)
}
