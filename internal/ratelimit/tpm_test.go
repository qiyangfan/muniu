// TPM 滑动窗口契约测试(测试先行域):跨窗滑动、边界、tpm=0 不限、并发 -race。
// 时间注入,完全确定。
package ratelimit

import (
	"sync"
	"testing"
	"time"
)

func TestTPM_跨窗滑动(t *testing.T) {
	w := NewTPM()
	now := time.Unix(3_000_000, 0)
	w.Record("k", 100, now)
	// 窗口内:总量已达 100/100 → 拒
	if w.Allow("k", 100, now.Add(30*time.Second)) {
		t.Fatal("窗口总量达上限应拒绝")
	}
	// 跨过 60s:t0 桶滑出窗口 → 恢复
	if !w.Allow("k", 100, now.Add(61*time.Second)) {
		t.Fatal("t0 桶滑出后应放行")
	}
}

func TestTPM_边界恰差1token(t *testing.T) {
	w := NewTPM()
	now := time.Unix(3_100_000, 0)
	w.Record("k", 99, now)
	if !w.Allow("k", 100, now.Add(5*time.Second)) {
		t.Fatal("99/100 应放行")
	}
	w.Record("k", 1, now.Add(5*time.Second))
	if w.Allow("k", 100, now.Add(6*time.Second)) {
		t.Fatal("100/100 应拒绝")
	}
}

func TestTPM_limit0不限(t *testing.T) {
	w := NewTPM()
	now := time.Unix(3_200_000, 0)
	w.Record("k", 1_000_000, now)
	for i := 0; i < 100; i++ {
		if !w.Allow("k", 0, now) {
			t.Fatal("tpm_limit=0 视为不限")
		}
	}
}

func TestTPM_key隔离(t *testing.T) {
	w := NewTPM()
	now := time.Unix(3_300_000, 0)
	w.Record("a", 100, now)
	if w.Allow("a", 100, now) {
		t.Fatal("a 应拒绝")
	}
	if !w.Allow("b", 100, now) {
		t.Fatal("b 不受 a 影响")
	}
}

func TestTPM_并发记账不race且总量正确(t *testing.T) {
	w := NewTPM()
	now := time.Unix(3_400_000, 0)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				w.Record("k", 1, now)
			}
		}()
	}
	wg.Wait()
	if w.windowTotal("k", now) != 800 {
		t.Fatalf("并发记账总量不符: %d", w.windowTotal("k", now))
	}
}
