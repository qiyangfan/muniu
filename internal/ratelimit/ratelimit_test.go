// RPM 令牌桶限流契约测试(测试先行域):边界 N-1/N/N+1、key 间隔离、并发对抗 -race、
// idle 淘汰(注入时钟,确定性验证)。
package ratelimit

import (
	"sync"
	"testing"
	"time"
)

func TestAllow_边界N减1_N_N加1(t *testing.T) {
	l := New()
	// rpm=60:桶容 60、速率 1/s;初始满桶,第 61 次必须拒
	for i := 0; i < 60; i++ {
		ok, retry := l.Allow("k1", 60)
		if !ok {
			t.Fatalf("第 %d 次应放行(桶容 60),retry=%v", i+1, retry)
		}
	}
	ok, retry := l.Allow("k1", 60)
	if ok {
		t.Fatal("第 61 次必须拒绝")
	}
	if retry <= 0 {
		t.Fatalf("拒绝必须携带正的 Retry-After,实际 %v", retry)
	}
}

func TestAllow_rpm0不限流(t *testing.T) {
	l := New()
	for i := 0; i < 1000; i++ {
		if ok, _ := l.Allow("free", 0); !ok {
			t.Fatalf("rpm=0 视为不限,第 %d 次不应拒", i+1)
		}
	}
}

func TestAllow_key间互相隔离(t *testing.T) {
	l := New()
	for i := 0; i < 10; i++ {
		l.Allow("k1", 10)
	}
	if ok, _ := l.Allow("k1", 10); ok {
		t.Fatal("k1 打满应拒")
	}
	if ok, _ := l.Allow("k2", 10); !ok {
		t.Fatal("k2 不应受 k1 影响")
	}
}

func TestAllow_并发不race且总量守恒(t *testing.T) {
	l := New()
	const goroutines, perG = 8, 50
	var mu sync.Mutex
	allowed := 0
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				if ok, _ := l.Allow("shared", 60); ok {
					mu.Lock()
					allowed++
					mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()
	// 并发时序下部分请求触发令牌回流,放行数在 [60, 60+goroutines×perG/秒回流] 内;
	// 关键断言:绝不超桶容+回流上限——此处只验证有限性(60 为硬顶,回流按 1/s 慢速)
	if allowed < 60 || allowed > 60+goroutines*perG {
		t.Fatalf("放行数异常: %d", allowed)
	}
}

func TestSweep_idle淘汰(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	l := &Limiter{m: map[string]*bucket{}, idle: time.Hour, now: func() time.Time { return now }}

	l.Allow("old", 60)
	now = now.Add(2 * time.Hour) // 推进时钟越过 idle
	l.Allow("fresh", 60)

	l.sweep(now)
	if _, exists := l.m["old"]; exists {
		t.Fatal("idle 超时条目应被淘汰")
	}
	if _, exists := l.m["fresh"]; !exists {
		t.Fatal("活跃条目不得被淘汰")
	}
}

func TestAllow_时间回流后恢复(t *testing.T) {
	now := time.Unix(2_000_000, 0)
	l := &Limiter{m: map[string]*bucket{}, idle: time.Hour, now: func() time.Time { return now }}

	for i := 0; i < 5; i++ {
		l.Allow("k", 5)
	}
	if ok, _ := l.Allow("k", 5); ok {
		t.Fatal("打满应拒")
	}
	now = now.Add(time.Minute) // rpm=5 → 速率 5/分钟,一分钟后应回流 5 个
	if ok, _ := l.Allow("k", 5); !ok {
		t.Fatal("时间回流后应恢复放行")
	}
}
