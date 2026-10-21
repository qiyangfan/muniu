// Failover 判定表：「适配器返回的 ErrorClass × 是否已过首 token × 流式与否」→ 动作。
// 纯函数零 IO。
package failover

// Action 判定表输出的动作。
type Action string

// 动作常量。
const (
	ActionNextCandidate Action = "next_candidate" // 换下一候选（重放安全）
	ActionAbort         Action = "abort"          // 断言出口：流内错误帧 + [DONE]，关流
	ActionReturn        Action = "return"         // 不换道直接返回（参数问题换家也会拒）
	ActionSilent        Action = "silent"         // 静默退出（客户端断连）
)

// Decide 查判定表。非流式没有「已过首 token」态（首字节前全部可重放），stream=false 时
// pastFirstToken 恒按 false 处理。
// 补充格：已过首 token 时 dial_fail/timeout_first 定义上不应出现，
// 防御归 abort；mid_stream_fail 未过首 token（首 token 前断流）零输出可重放，归 next_candidate；
// unknown 按 upstream_5xx 同待遇。
func Decide(class ErrorClass, pastFirstToken bool, stream bool) Action {
	if !stream {
		pastFirstToken = false
	}
	switch class {
	case ClassDialFail, ClassTimeoutFirst:
		if pastFirstToken {
			return ActionAbort // 防御格：定义上不可出现
		}
		return ActionNextCandidate
	case ClassUpstream5xx, ClassRateLimited, ClassAuthFailed, ClassUnknown:
		if pastFirstToken {
			return ActionAbort
		}
		return ActionNextCandidate
	case ClassBadRequest:
		if pastFirstToken {
			return ActionAbort
		}
		return ActionReturn
	case ClassMidStreamFail:
		if pastFirstToken {
			return ActionAbort
		}
		return ActionNextCandidate
	case ClassContextCanceled, ClassCostBudget:
		// 网关策略类与客户端断连同族：静默终结、不换道（同别名跨候选价目相同，换道无解），
		// 不计 key 失败（调用方 reportKeyOutcome 同样不回写）
		return ActionSilent
	default:
		if pastFirstToken {
			return ActionAbort
		}
		return ActionNextCandidate
	}
}
