// Package keypool 承载 per-provider 多 key 调度与三态状态机：smooth WRR 选择；
// 健康度 = 最近 20 次结果 EMA 成功率，有效权重 = weight × max(0.1, 成功率)，下限防饿死。
// 冷却/禁用的进入与退出条件：
//
//	429 → 冷却 Retry-After（缺省 60s，连续次数指数 ×2，上限 30min）
//	5xx → 连续 3 次 → 冷却 30s（成功即清零连续计数）
//	401/403 → 禁用（人工介入级，仅进程重启/改配置退出）
//
// 时间一律由调用方以 now 参数注入，可确定性测试；并发安全：mutex 内完成选择，O(n)。
package keypool

import (
	"sync"
	"time"
)

// Outcome 上游调用结果分类，报告给池以更新状态与健康度。
type Outcome int

// 结果分类常量。Failure 为通用失败（拨号/超时/断流等），只记 EMA，不触发任何冷却计数。
const (
	OutcomeSuccess     Outcome = iota
	OutcomeRateLimited         // 429
	OutcomeServerError         // 5xx（计数，连续 3 次冷却）
	OutcomeAuthFailed          // 401/403（禁用）
	OutcomeFailure             // 通用失败（仅 EMA）
)

// KeyInput 构造入参：一把 key 的标识、明文与权重。
type KeyInput struct {
	ID     string
	Value  string
	Weight int
}

// Key 被选中的键，对外仅暴露标识与明文；状态在池内，调用方经 Report 回填。
type Key struct {
	ID    string
	Value string
}

// emaWindow 健康度窗口：最近 20 次结果。
const emaWindow = 20

// 冷却/退避相关常量。
const (
	base429        = 60 * time.Minute / 60 // 60s
	max429         = 30 * time.Minute
	consec5xxLimit = 3
	cooldown5xx    = 30 * time.Second
	emaFloor       = 0.1
)

// keyState 单把 key 的全部状态，访问须持 Pool.mu。
type keyState struct {
	id, value string
	weight    float64
	current   float64 // smooth WRR 当前权重

	disabled      bool
	cooldownUntil time.Time
	consec429     int
	consec5xx     int

	ema     [emaWindow]bool // true=成功
	emaIdx  int
	emaFill int
}

// emaRate 成功率；样本不足按乐观 1.0，初始不惩罚。
func (k *keyState) emaRate() float64 {
	if k.emaFill == 0 {
		return 1
	}
	ok := 0
	for i := 0; i < k.emaFill; i++ {
		if k.ema[i] {
			ok++
		}
	}
	return float64(ok) / float64(k.emaFill)
}

// Pool 单 provider 的 key 池，进程内单例，装配层构建。
type Pool struct {
	mu   sync.Mutex
	keys []*keyState
}

// NewPool 构造池；weight 缺省 1。
func NewPool(inputs []KeyInput) *Pool {
	p := &Pool{}
	for _, in := range inputs {
		w := in.Weight
		if w <= 0 {
			w = 1
		}
		p.keys = append(p.keys, &keyState{id: in.ID, value: in.Value, weight: float64(w), current: float64(w)})
	}
	return p
}

// Pick 选一把可用 key：过滤禁用与冷却中，剩余按「有效权重 = weight × max(0.1, EMA)」
// 做 smooth WRR。全部不可用返回 nil（上层据此判定候选耗尽）；nil 池安全返回 nil。
func (p *Pool) Pick(now time.Time) *Key {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	total := 0.0
	best := -1
	bestCur := 0.0
	for i, k := range p.keys {
		if k.disabled || now.Before(k.cooldownUntil) {
			continue
		}
		eff := k.weight * emaRateFloor(k)
		k.current += eff // smooth WRR：每轮累加有效权重
		total += eff
		if best == -1 || k.current > bestCur {
			best, bestCur = i, k.current
		}
	}
	if best == -1 || total == 0 {
		return nil
	}
	k := p.keys[best]
	k.current -= total // 选中者扣减全池总权重，下轮让位
	return &Key{ID: k.id, Value: k.value}
}

// emaRateFloor EMA 成功率带下限，防饿死。
func emaRateFloor(k *keyState) float64 {
	r := k.emaRate()
	if r < emaFloor {
		return emaFloor
	}
	return r
}

// Report 回填调用结果并推进状态机。retryAfter 为上游 429 的 Retry-After，可为 0。
func (p *Pool) Report(k *Key, outcome Outcome, now time.Time, retryAfter time.Duration) {
	if p == nil || k == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	st := p.byID(k.ID)
	if st == nil {
		return
	}
	switch outcome {
	case OutcomeSuccess:
		st.consec429, st.consec5xx = 0, 0
		st.record(true)
	case OutcomeRateLimited:
		st.consec429++
		st.record(false)
		delay := retryAfter
		if delay <= 0 {
			// 指数退避：60s × 2^(连续次数-1)，封顶 30min
			delay = base429
			for i := 1; i < st.consec429 && delay < max429; i++ {
				delay *= 2
			}
			if delay > max429 {
				delay = max429
			}
		}
		if until := now.Add(delay); until.After(st.cooldownUntil) {
			st.cooldownUntil = until
		}
	case OutcomeServerError:
		st.consec5xx++
		st.record(false)
		if st.consec5xx >= consec5xxLimit {
			st.consec5xx = 0
			if until := now.Add(cooldown5xx); until.After(st.cooldownUntil) {
				st.cooldownUntil = until
			}
		}
	case OutcomeAuthFailed:
		st.disabled = true
		st.record(false)
	default:
		st.record(false)
	}
}

func (k *keyState) record(success bool) {
	k.ema[k.emaIdx] = success
	k.emaIdx = (k.emaIdx + 1) % emaWindow
	if k.emaFill < emaWindow {
		k.emaFill++
	}
}

// StateOf 键当前状态（观测/测试用）：healthy | cooldown | disabled。
func (p *Pool) StateOf(id string, now time.Time) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	k := p.byID(id)
	if k == nil {
		return "unknown"
	}
	switch {
	case k.disabled:
		return "disabled"
	case now.Before(k.cooldownUntil):
		return "cooldown"
	default:
		return "healthy"
	}
}

// CooldownRemain 剩余冷却时长，未冷却返回 0（观测/测试用）。
func (p *Pool) CooldownRemain(id string, now time.Time) time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	k := p.byID(id)
	if k == nil || now.After(k.cooldownUntil) {
		return 0
	}
	return k.cooldownUntil.Sub(now)
}

// EMARate 键的 EMA 成功率（观测/测试用）。
func (p *Pool) EMARate(id string) float64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	k := p.byID(id)
	if k == nil {
		return 0
	}
	return k.emaRate()
}

// KeyView 键的只读观测形态:余额观测等旁路消费逐 key 枚举用。
// 经 Snapshot 取得,不走 Pick/Report,与数据面 WRR/冷却语义完全解耦。
type KeyView struct {
	ID       string
	Value    string
	Disabled bool
}

// Snapshot 池内全部键的只读快照,顺序稳定,零运行态副作用。
func (p *Pool) Snapshot() []KeyView {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]KeyView, 0, len(p.keys))
	for _, k := range p.keys {
		out = append(out, KeyView{ID: k.id, Value: k.value, Disabled: k.disabled})
	}
	return out
}

// byID 查键状态，调用方须持 mu。
func (p *Pool) byID(id string) *keyState {
	for _, k := range p.keys {
		if k.id == id {
			return k
		}
	}
	return nil
}

// StateCounts 全池状态计数（观测/metrics 用）：healthy/cooldown/disabled。
func (p *Pool) StateCounts(now time.Time) map[string]int {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := map[string]int{}
	for _, k := range p.keys {
		switch {
		case k.disabled:
			out["disabled"]++
		case now.Before(k.cooldownUntil):
			out["cooldown"]++
		default:
			out["healthy"]++
		}
	}
	return out
}
