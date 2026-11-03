// 欠费桶契约:阶梯 30m→1h→2h 封顶、成功清零、Pick 跳过、余额回写幂等/提前释放、桶间隔离。
package keypool

import (
	"testing"
	"time"
)

var creditT0 = time.Unix(1_800_000_000, 0)

func TestCreditBucket_阶梯推进封顶与成功清零(t *testing.T) {
	p := mkPool()
	k := &Key{ID: "a", Value: "sk-a"}
	p.Report(k, OutcomeCreditExhausted, creditT0, 0)
	if p.StateOf("a", creditT0.Add(time.Minute)) != "cooldown" {
		t.Fatal("欠费后应进入冷却")
	}
	if got := p.CooldownRemain("a", creditT0); got != 30*time.Minute {
		t.Fatalf("阶梯起点为整 30m: %v", got)
	}
	p.Report(k, OutcomeCreditExhausted, creditT0.Add(31*time.Minute), 0)
	if got := p.CooldownRemain("a", creditT0.Add(31*time.Minute)); got != time.Hour {
		t.Fatalf("第二步应 1h: %v", got)
	}
	p.Report(k, OutcomeCreditExhausted, creditT0.Add(91*time.Minute), 0)
	p.Report(k, OutcomeCreditExhausted, creditT0.Add(91*time.Minute+time.Second), 0)
	if got := p.CooldownRemain("a", creditT0.Add(91*time.Minute+time.Second)); got != 2*time.Hour {
		t.Fatalf("应封顶 2h: %v", got)
	}
	// 成功清零阶梯:冷却按自然到期恢复(与 429 桶同语义),后续欠费重新 30m 起步
	p.Report(k, OutcomeSuccess, creditT0.Add(93*time.Minute), 0)
	recoverAt := creditT0.Add(91*time.Minute + time.Second + 2*time.Hour)
	if p.StateOf("a", recoverAt.Add(-time.Second)) != "cooldown" {
		t.Fatal("自然到期前仍应冷却")
	}
	if p.StateOf("a", recoverAt) != "healthy" {
		t.Fatal("到期应自然恢复")
	}
	p.Report(k, OutcomeCreditExhausted, recoverAt, 0)
	if got := p.CooldownRemain("a", recoverAt); got != 30*time.Minute {
		t.Fatalf("清零后应回 30m 起步: %v", got)
	}
}

func TestCreditBucket_Pick跳过冷却中的key(t *testing.T) {
	p := NewPool([]KeyInput{{ID: "a", Value: "va", Weight: 100}, {ID: "b", Value: "vb", Weight: 1}})
	p.Report(&Key{ID: "a"}, OutcomeCreditExhausted, creditT0, 0)
	for i := 0; i < 5; i++ {
		if k := p.Pick(creditT0.Add(time.Second)); k == nil || k.ID != "b" {
			t.Fatalf("欠费冷却中的 a 不应被选中,实际 %+v", k)
		}
	}
}

func TestCreditBucket_余额回写幂等与提前释放(t *testing.T) {
	p := mkPool()
	p.RecordCreditExhausted("a", true, creditT0)
	if got := p.CooldownRemain("a", creditT0); got != 30*time.Minute {
		t.Fatalf("回写欠费应 30m 冷却: %v", got)
	}
	// 幂等:冷却中重复回写不得推进阶梯、不得刷新至时刻
	p.RecordCreditExhausted("a", true, creditT0.Add(10*time.Minute))
	if got := p.CooldownRemain("a", creditT0.Add(10*time.Minute)); got != 20*time.Minute {
		t.Fatalf("重复回写应保持原至时刻(余 20m): %v", got)
	}
	// 回写充值:立即释放且阶梯清零
	p.RecordCreditExhausted("a", false, creditT0.Add(15*time.Minute))
	if p.StateOf("a", creditT0.Add(15*time.Minute)) != "healthy" {
		t.Fatal("充值应立即解除欠费冷却")
	}
	p.Report(&Key{ID: "a"}, OutcomeCreditExhausted, creditT0.Add(16*time.Minute), 0)
	if got := p.CooldownRemain("a", creditT0.Add(16*time.Minute)); got != 30*time.Minute {
		t.Fatalf("释放后阶梯应清零回 30m: %v", got)
	}
	// 桶间隔离:充值回写不得解除限流桶冷却
	p2 := mkPool()
	p2.Report(&Key{ID: "a"}, OutcomeRateLimited, creditT0, 30*time.Minute)
	p2.RecordCreditExhausted("a", false, creditT0.Add(time.Minute))
	if p2.StateOf("a", creditT0.Add(time.Minute)) != "cooldown" {
		t.Fatal("充值回写不得解除限流桶冷却")
	}
}
