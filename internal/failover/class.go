// Package failover 承载候选迭代器与错误分类→动作判定(proxy-and-streaming「Failover 判定表」)。
// Phase 1 仅提供 ErrorClass 枚举(adapter.Classify 的返回契约);判定表与迭代器 Phase 3 落地。
// 本包必须保持纯函数零 IO(architecture「包结构」)。
package failover

// ErrorClass 上游错误分类,即判定表的行坐标。
type ErrorClass string

// 错误分类常量(语义见 proxy-and-streaming 判定表)。
const (
	ClassDialFail        ErrorClass = "dial_fail"        // 连不上、握手失败
	ClassTimeoutFirst    ErrorClass = "timeout_first"    // 首 token 超时
	ClassUpstream5xx     ErrorClass = "upstream_5xx"     // 上游 500/502/503
	ClassRateLimited     ErrorClass = "rate_limited_429" // 上游限频
	ClassAuthFailed      ErrorClass = "auth_failed_401"  // key 无效/无权
	ClassBadRequest      ErrorClass = "bad_request_400"  // 参数被上游拒
	ClassMidStreamFail   ErrorClass = "mid_stream_fail"  // 流中断/帧间超时/解析崩
	ClassContextCanceled ErrorClass = "context_canceled" // 客户端断连
	ClassUnknown         ErrorClass = "unknown"          // 未归类
)
