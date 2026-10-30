// 环形窗口契约:最低样本数门槛(小分母不判定)、阈值越线、过窗滚动重置。
package health

import (
	"testing"
	"time"
)

func TestRingWindow_样本门槛与越线(t *testing.T) {
	w := NewRingWindow(5*time.Minute, 20, 300)
	base := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	// 小分母:1/3 命中率高但样本不足,不判定
	if w.Add(3, 1, base) {
		t.Fatal("样本不足不得越线")
	}
	// 补样本到 20 个、5 次命中(25%):未越线
	if w.Add(17, 4, base.Add(time.Minute)) {
		t.Fatal("阈值之下不得越线")
	}
	// 第 6 次命中(20 样本 6 命中 = 30%):恰达阈值应越线(>=)
	if !w.Add(0, 1, base.Add(2*time.Minute)) {
		t.Fatal("达阈值应越线")
	}
	// 快照
	ratio, samples, breached := w.Snapshot(base.Add(2 * time.Minute))
	if !breached || samples != 20 || ratio != 300 {
		t.Fatalf("快照不符: ratio=%d samples=%d breached=%v", ratio, samples, breached)
	}
}

func TestRingWindow_过窗滚动重置(t *testing.T) {
	w := NewRingWindow(5*time.Minute, 2, 300)
	base := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	_ = w.Add(10, 10, base) // 100% 越线态
	// 过窗后重置:零样本不越线
	if w.Add(1, 0, base.Add(6*time.Minute)) {
		t.Fatal("过窗重置后不得沿用旧越线态")
	}
	_, samples, breached := w.Snapshot(base.Add(6 * time.Minute))
	if breached || samples != 1 {
		t.Fatalf("过窗重置失真: samples=%d breached=%v", samples, breached)
	}
}
