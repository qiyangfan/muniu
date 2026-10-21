// Package failover 定义上游错误分类与候选换道规则，ErrorClass 为适配器归一后的上游错误类型。
package failover

// ErrorClass 上游错误分类，即判定表的行坐标。
type ErrorClass string

// 错误分类常量。
const (
	ClassDialFail        ErrorClass = "dial_fail"            // 连不上、握手失败
	ClassTimeoutFirst    ErrorClass = "timeout_first"        // 首 token 超时
	ClassUpstream5xx     ErrorClass = "upstream_5xx"         // 上游 500/502/503
	ClassRateLimited     ErrorClass = "rate_limited_429"     // 上游限频
	ClassAuthFailed      ErrorClass = "auth_failed_401"      // key 无效/无权
	ClassBadRequest      ErrorClass = "bad_request_400"      // 参数被上游拒
	ClassMidStreamFail   ErrorClass = "mid_stream_fail"      // 流中断/帧间超时/解析崩
	ClassContextCanceled ErrorClass = "context_canceled"     // 客户端断连
	ClassCostBudget      ErrorClass = "cost_budget_exceeded" // 网关策略：单请求成本预算超限，非上游质量问题
	ClassUnknown         ErrorClass = "unknown"              // 未归类
)
