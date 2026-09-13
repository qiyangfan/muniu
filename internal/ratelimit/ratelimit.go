// Package limit 承载 RPM 令牌桶限流(traffic-governance「限流」):
// 维度 per apikey,桶容=rpm、速率=rpm/分钟,硬闸拒绝(Phase 3 加 TPM 与租户聚合档)。
// 惰性创建,idle 1h 淘汰;拒绝方语义 = 429 + Retry-After(换算在中间件层)。
package ratelimit

import (
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Limiter per-apikey 令牌桶集合(进程内单例,装配层持有)。
type Limiter struct {
	mu        sync.Mutex
	m         map[string]*bucket
	idle      time.Duration    // 条目空闲淘汰阈值(1h)
	now       func() time.Time // 时钟源(测试注入)
	lastSweep time.Time
}

// bucket 单个 apikey 的桶与其最近活跃时刻;访问须持 l.mu。
type bucket struct {
	lim      *rate.Limiter
	lastSeen time.Time
}

// New 构造限流器(idle 1h)。
func New() *Limiter {
	return newWithIdle(time.Hour)
}

// NewWithClock 注入时钟源(语义对拍/确定性测试用;时间序列驱动)。
func NewWithClock(now func() time.Time) *Limiter {
	return &Limiter{m: map[string]*bucket{}, idle: time.Hour, now: now}
}

// newWithIdle 测试可注入淘汰阈值与时钟。
func newWithIdle(idle time.Duration) *Limiter {
	return &Limiter{m: map[string]*bucket{}, idle: idle, now: time.Now}
}

// Allow 判定该 apikey 本次请求是否放行。
// rpm<=0 视为不限;返回 (是否放行, 拒绝时的 Retry-After)。
// 硬闸语义:桶内无令牌即拒,不等待;预留即时取消,不污染后续判定。
func (l *Limiter) Allow(keyID string, rpm int64) (bool, time.Duration) {
	if rpm <= 0 {
		return true, 0
	}
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Sub(l.lastSweep) >= l.idle {
		l.sweepLocked(now)
	}
	b, ok := l.m[keyID]
	if !ok {
		// 桶容=rpm,速率=rpm/分钟:瞬时突发到 rpm,长时间均值不超
		b = &bucket{lim: rate.NewLimiter(rate.Every(time.Minute/time.Duration(rpm)), int(rpm))}
		l.m[keyID] = b
	}
	b.lastSeen = now

	res := b.lim.ReserveN(now, 1)
	if !res.OK() {
		// 超出桶容上限(瞬时超额):拒绝
		return false, time.Second
	}
	if d := res.DelayFrom(now); d > 0 {
		res.CancelAt(now) // 不等待:取消预留,保持硬闸语义
		return false, d
	}
	return true, 0
}

// sweepLocked 淘汰 idle 超时条目(调用方持 l.mu)。
func (l *Limiter) sweepLocked(now time.Time) {
	for k, b := range l.m {
		if now.Sub(b.lastSeen) > l.idle {
			delete(l.m, k)
		}
	}
	l.lastSweep = now
}

// sweep 测试入口。
func (l *Limiter) sweep(now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweepLocked(now)
}
