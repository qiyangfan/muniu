// keypool 测试：平滑加权轮询分布、三态状态机全迁移（429 指数冷却/5xx 连续三次/401 禁用）、
// EMA 健康度衰减、并发对抗 -race；时间一律外部注入，完全确定。
package keypool

import (
	"sync"
	"testing"
	"time"
)

var base = time.Unix(1_700_000_000, 0)

func mkPool() *Pool {
	return NewPool([]KeyInput{
		{ID: "a", Value: "sk-a", Weight: 70},
		{ID: "b", Value: "sk-b", Weight: 30},
	})
}

func TestWRR_分布按权重(t *testing.T) {
	p := mkPool()
	const n = 10_000
	count := map[string]int{}
	now := base
	for i := 0; i < n; i++ {
		k := p.Pick(now)
		if k == nil {
			t.Fatalf("第 %d 次无键可选", i)
		}
		count[k.ID]++
	}
	if a := count["a"]; a < 6650 || a > 7350 { // 70% ±5%
		t.Fatalf("权重 70 键分布越界: %d/%d", a, n)
	}
	if b := count["b"]; b < 2650 || b > 3350 { // 30% ±5%
		t.Fatalf("权重 30 键分布越界: %d/%d", b, n)
	}
}

func Test状态机_429冷却指数退避与上限(t *testing.T) {
	p := mkPool()
	ka := &Key{ID: "a"}
	now := base

	// 第一次 429：无 Retry-After → 基础 60s
	p.Report(ka, OutcomeRateLimited, now, 0)
	if st := p.StateOf("a", now); st != "cooldown" {
		t.Fatalf("429 后应冷却,实际 %s", st)
	}
	if d := p.CooldownRemain("a", now); d != 60*time.Second {
		t.Fatalf("首次冷却应 60s,实际 %v", d)
	}
	// 冷却期内不可选
	if k := p.Pick(now); k != nil && k.ID == "a" {
		t.Fatal("冷却中的键不得被选中")
	}
	// 到期恢复后再次 429：指数 ×2 → 120s
	now2 := now.Add(61 * time.Second)
	p.Report(ka, OutcomeRateLimited, now2, 0)
	if d := p.CooldownRemain("a", now2); d != 120*time.Second {
		t.Fatalf("连续第二次应 120s,实际 %v", d)
	}
	// 大量连续 429：封顶 30min
	n := now2
	for i := 0; i < 10; i++ {
		n = n.Add(31 * time.Minute)
		p.Report(ka, OutcomeRateLimited, n, 0)
	}
	if d := p.CooldownRemain("a", n); d > 30*time.Minute {
		t.Fatalf("冷却应封顶 30min,实际 %v", d)
	}
	// 显式 Retry-After 优先且大于退避值时采用
	n2 := n.Add(31 * time.Minute)
	p.Report(ka, OutcomeRateLimited, n2, 10*time.Minute)
	if d := p.CooldownRemain("a", n2); d != 10*time.Minute {
		t.Fatalf("Retry-After 应生效,实际 %v", d)
	}
}

func Test状态机_5xx连续三次冷却30秒(t *testing.T) {
	p := mkPool()
	ka := &Key{ID: "a"}
	now := base

	p.Report(ka, OutcomeServerError, now, 0)
	p.Report(ka, OutcomeServerError, now, 0)
	if st := p.StateOf("a", now); st != "healthy" {
		t.Fatalf("连续 2 次 5xx 应仍健康,实际 %s", st)
	}
	p.Report(ka, OutcomeServerError, now, 0)
	if st := p.StateOf("a", now); st != "cooldown" {
		t.Fatalf("连续 3 次 5xx 应冷却,实际 %s", st)
	}
	if d := p.CooldownRemain("a", now); d != 30*time.Second {
		t.Fatalf("5xx 冷却应 30s,实际 %v", d)
	}
	// 成功重置连续计数：冷却到期后，两次失败+一次成功+两次失败 → 不再触发冷却
	p.Report(ka, OutcomeServerError, now, 0)
	p.Report(ka, OutcomeServerError, now, 0)
	p.Report(ka, OutcomeSuccess, now, 0)
	p.Report(ka, OutcomeServerError, now, 0)
	p.Report(ka, OutcomeServerError, now, 0)
	after := now.Add(31 * time.Second) // 越过已进入的 30s 冷却
	if st := p.StateOf("a", after); st != "healthy" {
		t.Fatalf("成功应重置连续计数(冷却到期后不再触发),实际 %s", st)
	}
}

func Test状态机_401禁用不可恢复(t *testing.T) {
	p := mkPool()
	ka := &Key{ID: "a"}
	now := base
	p.Report(ka, OutcomeAuthFailed, now, 0)
	if st := p.StateOf("a", now.Add(time.Hour)); st != "disabled" {
		t.Fatalf("401 应禁用,实际 %s", st)
	}
	// 禁用永久生效：时间推进也不恢复
	for i := 0; i < 100; i++ {
		if k := p.Pick(now.Add(time.Duration(i) * time.Hour)); k != nil && k.ID == "a" {
			t.Fatal("禁用键不得被选中")
		}
	}
}

func Test冷却到期自然恢复(t *testing.T) {
	p := mkPool()
	ka := &Key{ID: "a"}
	now := base
	p.Report(ka, OutcomeRateLimited, now, time.Minute)
	if k := p.Pick(now.Add(30 * time.Second)); k != nil && k.ID == "a" {
		t.Fatal("冷却期内不得选中")
	}
	if k := p.Pick(now.Add(61 * time.Second)); k == nil || k.ID != "a" {
		t.Fatal("冷却到期应恢复选中")
	}
}

func TestEMA_健康度衰减与下限(t *testing.T) {
	p := mkPool()
	ka := &Key{ID: "a"}
	now := base
	// a 连续 20 次失败（穿插成功避免触发 5xx 连续冷却）
	for i := 0; i < 10; i++ {
		p.Report(ka, OutcomeServerError, now, 0)
		p.Report(ka, OutcomeSuccess, now, 0)
	}
	if r := p.EMARate("a"); r > 0.55 || r < 0.45 {
		t.Fatalf("EMA 成功率应约 0.5,实际 %v", r)
	}
	// 有效权重 = 70×0.5=35，b=30×1.0=30 → 选中分布应从 70/30 移向接近 35/30
	count := map[string]int{}
	for i := 0; i < 6500; i++ {
		k := p.Pick(now)
		count[k.ID]++
	}
	if count["a"]-count["b"] > 1300 { // 35:30 → 差约 500/6500，给边界余量
		t.Fatalf("EMA 衰减未生效,分布仍偏向 a: %v", count)
	}

	// 下限 0.1：成功率趋近 0 也不得饿死（仍按 10% 权重被选中）
	p2 := NewPool([]KeyInput{{ID: "x", Value: "v1", Weight: 100}, {ID: "y", Value: "v2", Weight: 100}})
	kx := &Key{ID: "x"}
	ky := &Key{ID: "y"}
	n := base
	for i := 0; i < 200; i++ { // 交替成功/失败会让 EMA≈0.5，改为大量失败+极少成功
		p2.Report(kx, OutcomeServerError, n, 0)
		if i%10 == 0 {
			p2.Report(kx, OutcomeSuccess, n, 0) // 打断连续 5xx
		}
		p2.Report(ky, OutcomeSuccess, n, 0)
	}
	pickedX := 0
	later := n.Add(time.Hour) // 读取时刻晚于可能存在的 30s 冷却，专测 EMA 衰减而非冷却
	for i := 0; i < 2000; i++ {
		if k := p2.Pick(later); k.ID == "x" {
			pickedX++
		}
	}
	if pickedX == 0 {
		t.Fatal("EMA 下限 0.1 防饿死:x 仍应被选中")
	}
	if r := p2.EMARate("x"); r > 0.15 {
		t.Fatalf("x 成功率应低于 0.15,实际 %v", r)
	}
}

func Test全键不可用_Pick返回nil(t *testing.T) {
	p := mkPool()
	now := base
	p.Report(&Key{ID: "a"}, OutcomeAuthFailed, now, 0)
	p.Report(&Key{ID: "b"}, OutcomeAuthFailed, now, 0)
	if k := p.Pick(now); k != nil {
		t.Fatal("全部禁用应返回 nil")
	}
}

func Test并发_Pick与Report无race(t *testing.T) {
	p := mkPool()
	now := base
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				k := p.Pick(now.Add(time.Duration(i) * time.Second))
				if k == nil {
					continue
				}
				switch (g + i) % 4 {
				case 0:
					p.Report(k, OutcomeSuccess, now, 0)
				case 1:
					p.Report(k, OutcomeRateLimited, now, 0)
				case 2:
					p.Report(k, OutcomeServerError, now, 0)
				default:
					p.Report(k, OutcomeSuccess, now, 0)
				}
			}
		}(g)
	}
	wg.Wait()
}

func TestSnapshot_只读枚举与数据面解耦(t *testing.T) {
	pool := NewPool([]KeyInput{{ID: "KEY_A", Value: "va", Weight: 1}, {ID: "KEY_B", Value: "vb", Weight: 3}})
	now := time.Now()
	// 禁用 KEY_A(401 语义),不影响 KEY_B
	pool.Report(&Key{ID: "KEY_A"}, OutcomeAuthFailed, now, 0)
	snap := pool.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("快照键数失真: %+v", snap)
	}
	byID := map[string]KeyView{}
	for _, kv := range snap {
		byID[kv.ID] = kv
	}
	if !byID["KEY_A"].Disabled || byID["KEY_A"].Value != "va" {
		t.Fatalf("KEY_A 观测失真: %+v", byID["KEY_A"])
	}
	if byID["KEY_B"].Disabled || byID["KEY_B"].Value != "vb" {
		t.Fatalf("KEY_B 观测失真: %+v", byID["KEY_B"])
	}
	// 只读性:多次 Snapshot 不改变 WRR 行为——KEY_B 权重 3,连续 Pick 应恒选 KEY_B
	for i := 0; i < 5; i++ {
		k := pool.Pick(now)
		if k == nil || k.ID != "KEY_B" {
			t.Fatalf("Snapshot 后 WRR 行为被扰动: %+v", k)
		}
	}
}

func TestSyncAccount_凭据原位替换与收缩恢复(t *testing.T) {
	p := NewPool([]KeyInput{{ID: "acc-1", Value: "at-old", Weight: 1}})
	now := base

	// 原位替换:后续 Pick 即取新凭据(零重启零拓扑)
	p.SyncAccount("acc-1", "at-new", true)
	if k := p.Pick(now); k == nil || k.Value != "at-new" {
		t.Fatalf("刷新后应取新凭据: %+v", k)
	}

	// 收缩:删除/终态 → 禁用,Pick 不再选中
	p.SyncAccount("acc-1", "", false)
	if got := p.StateOf("acc-1", now); got != "disabled" {
		t.Fatalf("收缩后应 disabled, got %s", got)
	}
	if k := p.Pick(now); k != nil {
		t.Fatalf("禁用成员不应被选中: %+v", k)
	}

	// 恢复:重新授权 → active 解除禁用,凭据同步更新
	p.SyncAccount("acc-1", "at-again", true)
	if got := p.StateOf("acc-1", now); got != "healthy" {
		t.Fatalf("恢复后应 healthy, got %s", got)
	}
	if k := p.Pick(now); k == nil || k.Value != "at-again" {
		t.Fatalf("恢复后应取新凭据: %+v", k)
	}

	// 未知 ID 零作用:不得注册为新成员、不得扰动既有成员(后置断言钉住)
	p.SyncAccount("nope", "x", true)
	if got := p.StateOf("nope", now); got != "unknown" {
		t.Fatalf("未知 ID 不得注册为池成员, got %s", got)
	}

	// 空 value + active:保旧值只解禁(value 守卫的另一腿)
	p.SyncAccount("acc-1", "", true)
	if got := p.StateOf("acc-1", now); got != "healthy" {
		t.Fatalf("空 value 同步应只动可用性, got %s", got)
	}
	if k := p.Pick(now); k == nil || k.Value != "at-again" {
		t.Fatalf("空 value 不得覆写存量凭据: %+v", k)
	}

	// 空池安全
	var nilPool *Pool
	nilPool.SyncAccount("acc-1", "x", true)
}

func TestSyncAccount_与冷却正交(t *testing.T) {
	// 账号同步不触碰冷却记账:限流冷却中的账号,同步 active 后仍在冷却(调度不变式零特例)
	p := NewPool([]KeyInput{{ID: "acc-1", Value: "at", Weight: 1}})
	now := base
	p.Report(&Key{ID: "acc-1"}, OutcomeRateLimited, now, 10*time.Minute)
	p.SyncAccount("acc-1", "at-2", true)
	if got := p.CooldownRemain("acc-1", now); got <= 0 {
		t.Fatalf("账号同步不得清除限流冷却: %v", got)
	}
	if k := p.Pick(now); k != nil {
		t.Fatal("冷却中账号不应被选中")
	}
}
