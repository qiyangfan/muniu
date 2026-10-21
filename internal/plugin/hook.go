// 请求体钩子编排：对入站 JSON body 依注册序执行全部启用插件，单次执行。
// 拒绝短路；fail_closed 失败短路，fail_open 失败放行，改写链式传递。
package plugin

import "context"

// ApplyResult 全链执行结论。
type ApplyResult struct {
	Body       []byte   // 最终请求体，可能被链式改写。
	ModifiedBy []string // 触发改写的插件名，供响应留痕头消费。
	RejectedBy string   // 拒绝插件名，非空表示请求被拒。
	Reason     string   // 拒绝理由。
	FailedBy   string   // containment 插件名，非空表示 fail_closed 已拦截，调用方回 500 信封。
}

// ApplyToBody 对 body 执行全部启用插件。无启用插件时仅一次快照分配、零拷贝返回。
func (r *Registry) ApplyToBody(ctx context.Context, body []byte) ApplyResult {
	snapshot := r.acquireSnapshot()
	defer r.releaseSnapshot(snapshot)
	if len(snapshot) == 0 {
		return ApplyResult{Body: body}
	}
	res := ApplyResult{Body: body}
	for _, p := range snapshot {
		v := p.HookRequestBody(ctx, res.Body)
		switch v.Kind {
		case VerdictKindModified:
			res.Body = v.Body
			res.ModifiedBy = append(res.ModifiedBy, p.Name())
		case VerdictKindRejected:
			res.RejectedBy = p.Name()
			res.Reason = v.Reason
			return res
		case VerdictKindFailed:
			// fail_open 已在 HookRequestBody 兑现为 Pass，这里必是 fail_closed。
			res.FailedBy = p.Name()
			res.Reason = v.Reason
			return res
		}
	}
	return res
}
