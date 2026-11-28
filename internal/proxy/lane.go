// 非 chat 推理 lane(embeddings/images/multimodal)的共性控制:
// 首 token 前预算、key 池耗尽分级、半开名额的安全释放。
package proxy

import (
	"fmt"
	"net/http"
	"time"

	"github.com/solvela/muniu/internal/failover"
	"github.com/solvela/muniu/internal/keypool"
	"github.com/solvela/muniu/internal/protocol"
)

// poolSkipTracker 候选循环的池跳过聚合:池级可用性只读采样,耗尽出口据此分级。
type poolSkipTracker struct {
	skipped  bool      // 至少一个候选因池不可用被跳过
	seen     bool      // 已合并至少一个池的可用性
	allRate  bool      // 全部池不可用原因均为限频/欠费计时(无禁用/5xx)
	earliest time.Time // 池内最早恢复时刻
}

func (t *poolSkipTracker) note(av keypool.PoolAvailability) {
	t.skipped = true
	if !t.seen {
		t.allRate = true
	}
	if !t.seen || (av.Earliest.IsZero() || (!t.earliest.IsZero() && av.Earliest.Before(t.earliest))) {
		t.earliest = av.Earliest
	}
	t.allRate = t.allRate && av.AllRateLimited
	t.seen = true
}

// laneAttemptState 非 chat 推理 lane 的跨候选状态。
type laneAttemptState struct {
	deadline     time.Time
	attempts     int // 真实上游尝试数(跳过不计)
	last         failover.ErrorClass
	oauthSkipped bool
	pool         poolSkipTracker
}

// newLaneAttemptState 建立请求级预算;零值截止表示不限。
func (p *Proxy) newLaneAttemptState() *laneAttemptState {
	st := &laneAttemptState{}
	if d := p.timeouts.FailoverDeadline; d > 0 {
		st.deadline = time.Now().Add(d)
	}
	return st
}

// laneBudgetExhausted 首 token 前阶段预算是否已用尽。
func (p *Proxy) laneBudgetExhausted(st *laneAttemptState) bool {
	if !st.deadline.IsZero() && !time.Now().Before(st.deadline) {
		return true
	}
	return p.timeouts.MaxAttempts > 0 && st.attempts >= p.timeouts.MaxAttempts
}

// finishLaneBudgetExhausted 预算命中:不再发起上游调用,给 504 自解释信封。
func (p *Proxy) finishLaneBudgetExhausted(tw *trackWriter, r *http.Request, st *laneAttemptState) {
	if tw.committed {
		return
	}
	p.writeError(tw, r, protocol.TypeTimeout, "failover_budget_exhausted",
		fmt.Sprintf("首 token 前换道预算已用尽(尝试 %d 次,最后错误: %s)", st.attempts, st.last))
}

// finishLanePoolExhausted 全候选仅因 key 池不可用而跳过时按限频/不可用分级;返回是否已写响应。
func (p *Proxy) finishLanePoolExhausted(tw *trackWriter, r *http.Request, st *laneAttemptState) bool {
	if !st.pool.skipped || st.last != "" || st.oauthSkipped {
		return false
	}
	setPoolRetryAfter(tw, st.pool.earliest)
	if st.pool.allRate {
		p.writeError(tw, r, protocol.TypeRateLimited, "keypool_rate_limited",
			"该别名的 key 均在限频/欠费冷却中,请按 Retry-After 稍后重试")
		return true
	}
	p.writeError(tw, r, protocol.TypeUnavailable, "keypool_exhausted",
		"该别名的 key 均不可用(冷却或禁用),请稍后重试")
	return true
}

// shouldRetrySameProviderKey key 级硬错误且未提交时,换同 provider 下一把 key。
func shouldRetrySameProviderKey(committed bool, class failover.ErrorClass, m *meterState) bool {
	return !committed && (failover.RetrySameProvider(class) || m.creditEvidence) &&
		failover.Decide(class, m.firstSet, m.stream) == failover.ActionNextCandidate
}

// runAttemptWithRelease attempt 的 panic 安全释放:a panic 时 defer 先于 server.Recover 回卷执行,
// 半开名额不会因单次尝试崩溃而永久泄漏。
func runAttemptWithRelease(release func(), fn func() (bool, failover.ErrorClass)) (bool, failover.ErrorClass) {
	if release != nil {
		defer release()
	}
	return fn()
}
