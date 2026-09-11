// 数据面指标插桩(ADR-0009:零依赖直写;未启用时全部零开销短路)。
package proxy

import "github.com/solvela/muniu/internal/metrics"

var (
	// RequestsTotal 转发请求计数 {model,status}。
	RequestsTotal = metrics.NewCounterVec("muniu_requests_total", "转发请求数", "model", "status")
	// UpstreamTotal 上游结果计数 {provider,class}。
	UpstreamTotal = metrics.NewCounterVec("muniu_upstream_total", "上游结果数(按错误分类)", "provider", "class")
	// RequestDuration 请求总耗时直方图(ms)。
	RequestDuration = metrics.NewHistogram("muniu_request_duration_ms", "请求总耗时(ms)", metrics.DefBuckets)
)
