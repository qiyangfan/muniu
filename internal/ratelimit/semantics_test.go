package ratelimit

import (
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// 语义测试集(共享契约):同一确定性时钟序列驱动两后端,判定布尔逐例一致,
// Retry-After 差 ≤1s(GCRA 与 x/time/rate 的浮点路径有理差异上界)。
// inmem 侧经 inmemShared 适配器(Limiter 时钟注入);redis 侧 miniredis。

// inmemShared 进程内实现的 SharedBackend 适配(装配缺省形态的测试投影)。
type inmemShared struct {
	rpm *Limiter
	tpm *TPM
}

func (s inmemShared) AllowRPM(keyID string, rpm int64, now time.Time) (bool, time.Duration) {
	return s.rpm.Allow(keyID, rpm)
}

func (s inmemShared) AllowTPM(keyID string, limit int64, now time.Time) bool {
	return s.tpm.Allow(keyID, limit, now)
}

func (s inmemShared) RecordTPM(keyID string, tokens int64, now time.Time) {
	s.tpm.Record(keyID, tokens, now)
}

func (inmemShared) Degraded() bool { return false }
func (inmemShared) Close() error   { return nil }

type clock struct{ t time.Time }

func (c *clock) Now() time.Time          { return c.t }
func (c *clock) Advance(d time.Duration) { c.t = c.t.Add(d) }

type verdict struct {
	allow bool
	retry time.Duration
}

// driveRPM 确定性 RPM 序列:同一秒内 burst 用尽→拒;跨秒恢复;key 隔离。
func driveRPM(t *testing.T, b SharedBackend, clk *clock) []verdict {
	t.Helper()
	var got []verdict
	for i := 0; i < 61; i++ { // rpm=60:前 60 放行,第 61 拒
		ok, retry := b.AllowRPM("k1", 60, clk.Now())
		got = append(got, verdict{ok, retry})
	}
	clk.Advance(2 * time.Second)
	ok, _ := b.AllowRPM("k1", 60, clk.Now())
	got = append(got, verdict{ok, 0}) // 期望:2 秒后恢复 1 个令牌
	ok2, _ := b.AllowRPM("k2", 60, clk.Now())
	got = append(got, verdict{ok2, 0}) // 期望:不同 key 独立放行
	return got
}

func assertRPMSemantics(t *testing.T, got []verdict) {
	t.Helper()
	for i := 0; i < 60; i++ {
		if !got[i].allow {
			t.Fatalf("burst 前 60 应放行,第 %d 拒", i+1)
		}
	}
	if got[60].allow {
		t.Fatal("第 61 次应拒(桶已空)")
	}
	if got[60].retry <= 0 {
		t.Fatal("拒绝时应给出正 Retry-After")
	}
	if !got[61].allow {
		t.Fatal("跨秒后应恢复放行(1 令牌)")
	}
	if !got[62].allow {
		t.Fatal("不同 key 应独立计数")
	}
}

func TestSharedSemanticsRPM_inmemVsRedis(t *testing.T) {
	mr := miniredis.RunT(t)
	clk := &clock{t: time.Unix(1_700_000_000, 0)}

	in := inmemShared{rpm: NewWithClock(clk.Now)}
	rs := NewRedisBackend(mr.Addr(), nil)

	gotIn := driveRPM(t, in, clk)
	clk.t = time.Unix(1_700_000_000, 0) // 重置同一序列
	gotRs := driveRPM(t, rs, clk)

	assertRPMSemantics(t, gotIn)
	for i := range gotIn {
		if gotIn[i].allow != gotRs[i].allow {
			t.Fatalf("判定不一致 @%d: inmem=%v redis=%v", i, gotIn[i].allow, gotRs[i].allow)
		}
		if !gotIn[i].allow && gotRs[i].allow {
			continue
		}
		if !gotIn[i].allow {
			diff := (gotIn[i].retry - gotRs[i].retry).Abs()
			if diff > time.Second {
				t.Fatalf("Retry-After 差超 1s @%d: inmem=%v redis=%v", i, gotIn[i].retry, gotRs[i].retry)
			}
		}
	}
}

// driveTPM 确定性 TPM 序列:前置判定→记账→窗口满拒→部分记账仍拒→窗口滑出恢复。
func driveTPM(t *testing.T, b SharedBackend, clk *clock) []bool {
	t.Helper()
	var got []bool
	got = append(got, b.AllowTPM("k1", 100, clk.Now())) // true(窗口空)
	b.RecordTPM("k1", 100, clk.Now())
	got = append(got, b.AllowTPM("k1", 100, clk.Now())) // false(窗口 100≥100)
	clk.Advance(2 * time.Second)
	b.RecordTPM("k1", 30, clk.Now())                    // t0 槽仍在窗:100+30=130
	got = append(got, b.AllowTPM("k1", 100, clk.Now())) // false
	clk.Advance(60 * time.Second)                       // t0/t2 槽全部滑出
	got = append(got, b.AllowTPM("k1", 100, clk.Now())) // true
	b.RecordTPM("k1", 0, clk.Now())                     // ≤0 忽略,不 panic
	got = append(got, b.AllowTPM("", 0, clk.Now()))     // limit=0 不限 → true
	return got
}

func TestSharedSemanticsTPM_inmemVsRedis(t *testing.T) {
	mr := miniredis.RunT(t)
	clk := &clock{t: time.Unix(1_700_000_000, 0)}

	in := inmemShared{tpm: NewTPM()}
	rs := NewRedisBackend(mr.Addr(), nil)

	gotIn := driveTPM(t, in, clk)
	clk.t = time.Unix(1_700_000_000, 0)
	gotRs := driveTPM(t, rs, clk)

	for i := range gotIn {
		if gotIn[i] != gotRs[i] {
			t.Fatalf("TPM 判定不一致 @%d: inmem=%v redis=%v", i, gotIn[i], gotRs[i])
		}
	}
	want := []bool{true, false, false, true, true}
	for i, w := range want {
		if gotIn[i] != w {
			t.Fatalf("TPM 语义 @%d 期望 %v 得 %v", i, w, gotIn[i])
		}
	}
}

// 降级纪律:redis 不可达 → degraded=true,回退判定仍可用、不 panic;
// 恢复探测:换到可用实例(或探测通过)后 degraded=false,共享视图恢复。
func TestRedisBackendDegradeAndRecover(t *testing.T) {
	// 1) 不可达地址 → 首操作触发降级,回退判定可用
	rs := NewRedisBackend("127.0.0.1:1", nil)
	defer func() { _ = rs.Close() }()
	ok, retry := rs.AllowRPM("k", 60, time.Now())
	if !ok || retry != 0 {
		t.Fatal("不可达时回退判定:空桶首次应放行(inmem 语义)")
	}
	if !rs.Degraded() {
		t.Fatal("操作失败应置 degraded")
	}
	if !rs.AllowTPM("k", 100, time.Now()) {
		t.Fatal("降级态回退判定应可用")
	}
	rs.RecordTPM("k", 5, time.Now()) // 降级态记账走 fallback,不 panic

	// 2) 恢复:换到可用实例,probe 通过后回共享视图
	mr := miniredis.RunT(t)
	rs.rdb = redis.NewClient(&redis.Options{Addr: mr.Addr(), DialTimeout: probeTimeout, ReadTimeout: probeTimeout, WriteTimeout: probeTimeout})
	rs.lastProbe.Store(0) // 允许立即探测
	rs.maybeProbe()
	if rs.Degraded() {
		t.Fatal("探测成功后应解除降级")
	}
	ok2, _ := rs.AllowRPM("k9", 60, time.Now())
	if !ok2 {
		t.Fatal("共享视图恢复后应可放行")
	}
}
