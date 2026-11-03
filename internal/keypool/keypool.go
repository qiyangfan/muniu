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
// OutcomeCreditExhausted 恒追加在尾部（持久化与调用点零扰动）：上游欠费证据（402 /
// insufficient_quota 封闭白名单），独立于限流桶的阶梯冷却，充值即恢复的暂态。
const (
	OutcomeSuccess         Outcome = iota
	OutcomeRateLimited             // 429
	OutcomeServerError             // 5xx（计数，连续 3 次冷却）
	OutcomeAuthFailed              // 401/403（禁用）
	OutcomeFailure                 // 通用失败（仅 EMA）
	OutcomeCreditExhausted         // 欠费（独立桶：30m→1h→2h 封顶阶梯，余额回写可提前释放）
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

	// 欠费桶阶梯:30 分钟起步、连续 ×2、2 小时封顶。封顶只兜「无余额源渠道」的自愈上界——
	// API key 充值即恢复(订阅窗口世界的 7d 阶梯不适用),有余额源的 key 由回写提前释放。
	creditBase   = 30 * time.Minute
	creditMax    = 2 * time.Hour
	creditLadder = 2
	// persistMinCooldown 达到此值的冷却才落库(欠费桶全程、429 桶后段);瞬态(5xx 30s/
	// 429 前段)不落库防写放大。
	persistMinCooldown = 5 * time.Minute
)

// 冷却桶标签:欠费回写(充值释放)只作用于 credit 桶;持久化按桶恢复阶梯计数。
const (
	bucketNone   = ""
	bucketCredit = "credit"
	bucketRate   = "rate"
)

// keyState 单把 key 的全部状态，访问须持 Pool.mu。
type keyState struct {
	id, value string
	weight    float64
	current   float64 // smooth WRR 当前权重

	disabled       bool
	cooldownUntil  time.Time
	cooldownBucket string // bucketNone | bucketCredit | bucketRate；欠费回写只作用于 credit 桶
	consec429      int
	consec5xx      int
	consecCredit   int // 欠费桶连续计数（阶梯推进依据），成功或充值回写清零

	ema     [emaWindow]bool // true=成功
	emaIdx  int
	emaFill int

	persistSeq int64 // 持久化事件单调序号（锁内递增；重启由行播种，守卫锁外落库乱序）
}

// CooldownRecord 冷却持久化记录形态（store 行 ↔ 池状态双向载体）。
type CooldownRecord struct {
	KeyID  string
	Bucket string // credit | rate
	Until  time.Time
	Consec int
	Seq    int64 // 事件单调序号（乱序守卫依据；重启时由行播种进 keyState）
}

// CooldownSink 冷却持久化出口（装配注入 store 实现）：仅在 ≥persistMinCooldown 的状态
// 迁移时调用，失败由实现方留痕，池不重试、不阻塞热路径、绝不在持锁状态下写入。
// seq 为该键事件单调序号（锁内递增），store 侧据此裁决锁外落库的完成乱序。
type CooldownSink interface {
	SaveCooldown(provider, keyID, bucket string, until time.Time, consec int, seq int64) error
}

// WithSink 注入持久化出口并返回池本身（装配链式）。provider 为行主键的一半。
func (p *Pool) WithSink(provider string, sink CooldownSink) *Pool {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.provider, p.sink = provider, sink
	return p
}

// LoadCooldowns 启动恢复：仅接受未到期的记录恢复进池（bucket/至时刻/阶梯计数一并恢复）。
func (p *Pool) LoadCooldowns(rows []CooldownRecord, now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, r := range rows {
		k := p.byID(r.KeyID)
		if k == nil {
			continue
		}
		// 序号播种先于过期过滤:过期行同样抬高基线,否则重启后新事件被 DB 旧守卫压住
		if r.Seq > k.persistSeq {
			k.persistSeq = r.Seq
		}
		if !r.Until.After(now) {
			continue
		}
		k.cooldownUntil, k.cooldownBucket = r.Until, r.Bucket
		switch r.Bucket {
		case bucketCredit:
			k.consecCredit = r.Consec
		case bucketRate:
			k.consec429 = r.Consec
		}
	}
}

// KeyIDs 池内全部键 ID（拓扑替换后 store 孤儿行对账清理用），顺序稳定。
func (p *Pool) KeyIDs() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, 0, len(p.keys))
	for _, k := range p.keys {
		out = append(out, k.id)
	}
	return out
}

// RecordCreditExhausted 余额回写联动（幂等）：
//   - exhausted=true：欠费进入/推进欠费桶；冷却中重复回写不推进阶梯、不刷新至时刻；
//   - exhausted=false：充值即恢复——欠费桶冷却立即解除且阶梯清零；rate 桶不受影响。
//   - 未知 keyID 零作用；余额事件不写 EMA（评估结论与请求质量正交）。
func (p *Pool) RecordCreditExhausted(keyID string, exhausted bool, now time.Time) {
	if p == nil {
		return
	}
	p.mu.Lock()
	k := p.byID(keyID)
	if k == nil {
		p.mu.Unlock()
		return
	}
	var ev *cooldownEvent
	if exhausted {
		if k.cooldownBucket == bucketCredit && now.Before(k.cooldownUntil) {
			p.mu.Unlock()
			return // 幂等：冷却中重复回写零作用
		}
		k.consecCredit++
		delay := creditBase
		for i := 1; i < k.consecCredit && delay < creditMax; i++ {
			delay *= creditLadder
		}
		if delay > creditMax {
			delay = creditMax
		}
		k.cooldownBucket = bucketCredit
		if until := now.Add(delay); until.After(k.cooldownUntil) {
			k.cooldownUntil = until
		}
		ev = p.persistEventLocked(k, now)
	} else {
		k.consecCredit = 0
		if k.cooldownBucket == bucketCredit {
			k.cooldownBucket, k.cooldownUntil = bucketNone, time.Time{}
			ev = p.persistEventLocked(k, now) // 清除语义：bucket 空 → store 删行
		}
	}
	p.mu.Unlock()
	p.persist(ev)
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
	mu       sync.Mutex
	keys     []*keyState
	provider string // 持久化行主键一半（WithSink 注入；装配期一次定型，此后只读）
	sink     CooldownSink
}

// cooldownEvent 一次需要落库的冷却状态迁移（≥persistMinCooldown 或清除语义）。
type cooldownEvent struct {
	keyID  string
	bucket string
	until  time.Time
	consec int
	seq    int64 // 事件单调序号(乱序守卫)
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
// ≥persistMinCooldown 的桶迁移在锁外同步落库（低频事件 + µs 级 UPSERT，失败由 sink 留痕）。
func (p *Pool) Report(k *Key, outcome Outcome, now time.Time, retryAfter time.Duration) {
	if p == nil || k == nil {
		return
	}
	p.mu.Lock()
	st := p.byID(k.ID)
	if st == nil {
		p.mu.Unlock()
		return
	}
	var ev *cooldownEvent
	switch outcome {
	case OutcomeSuccess:
		st.consec429, st.consec5xx, st.consecCredit = 0, 0, 0
		if now.After(st.cooldownUntil) {
			st.cooldownBucket = bucketNone
		}
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
		st.cooldownBucket = bucketRate
		if until := now.Add(delay); until.After(st.cooldownUntil) {
			st.cooldownUntil = until
		}
		ev = p.persistEventLocked(st, now)
	case OutcomeCreditExhausted:
		st.consecCredit++
		st.record(false)
		delay := creditBase
		for i := 1; i < st.consecCredit && delay < creditMax; i++ {
			delay *= creditLadder
		}
		if delay > creditMax {
			delay = creditMax
		}
		st.cooldownBucket = bucketCredit
		if until := now.Add(delay); until.After(st.cooldownUntil) {
			st.cooldownUntil = until
		}
		ev = p.persistEventLocked(st, now)
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
	p.mu.Unlock()
	p.persist(ev)
}

// persist 持久化事件在锁外同步执行（调用方已 Unlock；provider/sink 装配期定型后只读）。
// 同步的代价边界:发生点在响应已写出后的 handler 尾部,不占客户端时延;与计量共用单连接,
// 写冲突最坏等待 busy_timeout(5s);事件低频(欠费桶全程/rate 长冷却),异步 sink 的
// 生命周期与丢弃复杂度不值。失败静默交 sink 留痕:恢复窗口 = 下一次迁移或重启加载。
// ev.seq 经 store 的 ev_seq 守卫裁决锁外落库乱序(迟到的旧事件被行级忽略)。
func (p *Pool) persist(ev *cooldownEvent) {
	if ev == nil || p.sink == nil {
		return
	}
	_ = p.sink.SaveCooldown(p.provider, ev.keyID, ev.bucket, ev.until, ev.consec, ev.seq)
}

// persistEventLocked 持锁判定本次迁移是否需要落库：欠费桶全程落库（阶梯与至时刻是
// 重启恢复的必要状态）；rate 桶仅 ≥persistMinCooldown 的长冷却落库；清除事件恒落库删行。
func (p *Pool) persistEventLocked(st *keyState, now time.Time) *cooldownEvent {
	if p.sink == nil {
		return nil
	}
	st.persistSeq++ // 锁内单调递增:store 侧 ev_seq 守卫据此裁决锁外落库的完成乱序
	if st.cooldownBucket == bucketNone {
		return &cooldownEvent{keyID: st.id, bucket: bucketNone, seq: st.persistSeq}
	}
	if st.cooldownBucket == bucketCredit || st.cooldownUntil.Sub(now) >= persistMinCooldown {
		consec := st.consecCredit
		if st.cooldownBucket == bucketRate {
			consec = st.consec429
		}
		return &cooldownEvent{keyID: st.id, bucket: st.cooldownBucket, until: st.cooldownUntil, consec: consec, seq: st.persistSeq}
	}
	return nil
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

// PickWithHint 会话亲和提示选中(ADR-0020):hint 键健康(非禁用/冷却)时直取——亲和
// 是对既有分配的复用,不消耗 WRR 虚拟权重记账;hint 空/未知/不健康退回 Pick。
func (p *Pool) PickWithHint(hintKeyID string, now time.Time) *Key {
	if p == nil || hintKeyID == "" {
		return p.Pick(now)
	}
	p.mu.Lock()
	for _, k := range p.keys {
		if k.id == hintKeyID && !k.disabled && !now.Before(k.cooldownUntil) {
			p.mu.Unlock()
			return &Key{ID: k.id, Value: k.value}
		}
	}
	p.mu.Unlock()
	return p.Pick(now)
}
