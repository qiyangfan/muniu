package health

import (
	"sync"
	"time"
)

// RingWindow 定长时间窗计数器:过窗即滚动重置,统计「命中/总量」比与越线判定。
// 别名换道率窗口与渠道被动失败窗口共用同一形态;样本不足时诚实不判定(小分母防误报)。
type RingWindow struct {
	mu             sync.Mutex
	window         time.Duration
	minSamples     int
	thresholdMilli int64
	start          time.Time
	total          int
	hit            int
}

// NewRingWindow 构造;window<=0 时缺省 5 分钟,minSamples<1 时缺省 1。
func NewRingWindow(window time.Duration, minSamples int, thresholdMilli int64) *RingWindow {
	if window <= 0 {
		window = 5 * time.Minute
	}
	if minSamples < 1 {
		minSamples = 1
	}
	return &RingWindow{window: window, minSamples: minSamples, thresholdMilli: thresholdMilli}
}

// Add 计入 total/hit 增量,过窗先滚动;返回当前窗口是否越线
// (样本达最低样本数且命中比 ≥ 阈值毫单位)。
func (w *RingWindow) Add(total, hit int, now time.Time) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.start.IsZero() || now.Sub(w.start) >= w.window {
		w.start = now
		w.total = 0
		w.hit = 0
	}
	w.total += total
	w.hit += hit
	return w.breachedLocked()
}

// Snapshot 当前窗口快照(比率毫单位/样本数/是否越线);零样本返回 samples=0。
func (w *RingWindow) Snapshot(now time.Time) (int64, int, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.start.IsZero() || now.Sub(w.start) >= w.window {
		return 0, 0, false
	}
	return w.ratioLocked(), w.total, w.breachedLocked()
}

func (w *RingWindow) breachedLocked() bool {
	if w.total < w.minSamples {
		return false
	}
	return w.ratioLocked() >= w.thresholdMilli
}

func (w *RingWindow) ratioLocked() int64 {
	if w.total <= 0 {
		return 0
	}
	return int64(w.hit) * 1000 / int64(w.total)
}
