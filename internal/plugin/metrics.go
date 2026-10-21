// per-plugin 指标，零依赖直写；未启用时全部零开销短路。
package plugin

import "github.com/solvela/muniu/internal/metrics"

var (
	// callsTotal 插件调用计数 {plugin,verdict}(verdict: pass/modified/rejected/failed)。
	callsTotal = metrics.NewCounterVec("muniu_plugin_calls_total", "插件调用计数", "plugin", "verdict")
	// callDuration 单次调用耗时直方图(ms)。
	callDuration = metrics.NewHistogram("muniu_plugin_call_duration_ms", "插件单次调用耗时(ms)", metrics.DefBuckets)
	// containmentTotal containment 事件计数 {plugin,kind}。
	containmentTotal = metrics.NewCounterVec("muniu_plugin_containment_total", "插件 containment 事件数", "plugin", "kind")
)
