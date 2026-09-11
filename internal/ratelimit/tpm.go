// TPM 滑动窗口(traffic-governance「限流」):每 key 60×1s 环形桶,计数已完成请求的 token 数;
// 新请求前窗口总量 ≥ tpm_limit → 拒(429,中间件层出信封);tpm_limit=0 不限。
// 固有近似(契约,如实声明):在途请求 token 未计,误差 ≤ 单窗口并发量——宁可记账精确、
// 偶发小额超限,不做预留式伪精确。
package ratelimit

import (
	"sync"
	"time"
)

// tpmSlots 环形桶数(60 秒窗口)。
const tpmSlots = 60

// TPM 每 key 的 token 滑动窗口(进程内单例)。
type TPM struct {
	mu sync.Mutex
	m  map[string]*tpmRing
}

// tpmRing 环形桶:slotSecond 记录各槽对应的秒戳(过期槽惰性重置)。
type tpmRing struct {
	slotSecond [tpmSlots]int64
	tokens     [tpmSlots]int64
}

// NewTPM 构造。
func NewTPM() *TPM { return &TPM{m: map[string]*tpmRing{}} }

// Allow 判定该 key 新请求是否放行:窗口(含当前秒)总量 ≥ limit → 拒。limit<=0 不限。
func (t *TPM) Allow(keyID string, limit int64, now time.Time) bool {
	if limit <= 0 {
		return true
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	r := t.ring(keyID, now)
	total := int64(0)
	for i := 0; i < tpmSlots; i++ {
		total += r.windowTokens(r.slotSecond[i], now)
	}
	return total < limit
}

// Record 请求完成后记账 token 数(已完成的才算,契约)。
func (t *TPM) Record(keyID string, tokens int64, now time.Time) {
	if tokens <= 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	r := t.ring(keyID, now)
	r.add(now.Unix(), tokens)
}

// windowTotal 窗口总 token(观测/测试用)。
func (t *TPM) windowTotal(keyID string, now time.Time) int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	r, ok := t.m[keyID]
	if !ok {
		return 0
	}
	total := int64(0)
	for i := 0; i < tpmSlots; i++ {
		total += r.windowTokens(r.slotSecond[i], now)
	}
	return total
}

// ring 取(必要时建)key 的环形窗口并推进当前秒槽。
func (t *TPM) ring(keyID string, now time.Time) *tpmRing {
	r, ok := t.m[keyID]
	if !ok {
		r = &tpmRing{}
		t.m[keyID] = r
	}
	_ = r.windowTokens(now.Unix(), now) // 触发当前秒槽惰性重置
	return r
}

// windowTokens 若槽属于当前 60s 窗口则返回其计数,否则视为 0(过期槽由 add 时重置)。
func (r *tpmRing) windowTokens(slotSecond int64, now time.Time) int64 {
	if now.Unix()-slotSecond >= tpmSlots {
		return 0 // 过期槽
	}
	// 找到槽内计数(槽与秒一一对应,过期即 0)
	for i := 0; i < tpmSlots; i++ {
		if r.slotSecond[i] == slotSecond {
			return r.tokens[i]
		}
	}
	return 0
}

// add 向指定秒的桶累加(过期槽先重置)。
func (r *tpmRing) add(unixSec int64, tokens int64) {
	slot := unixSec % tpmSlots
	if r.slotSecond[slot] != unixSec {
		r.slotSecond[slot] = unixSec
		r.tokens[slot] = 0
	}
	r.tokens[slot] += tokens
}
