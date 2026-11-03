package keypool

import (
	"testing"
	"time"
)

// TestPickWithHint_亲和提示 亲和命中键健康 → 直取(不耗 WRR 记账);hint 空/未知/冷却
// → 退回 Pick;亲和键冷却让位后恢复可再命中。
func TestPickWithHint_亲和提示(t *testing.T) {
	p := NewPool([]KeyInput{{ID: "a", Value: "va", Weight: 100}, {ID: "b", Value: "vb", Weight: 1}})
	now := time.Unix(2_000_000_000, 0)

	if k := p.PickWithHint("a", now); k == nil || k.ID != "a" {
		t.Fatalf("健康 hint 应直取: %+v", k)
	}
	// hint 未知/空 → 退回 Pick(必返可用键)
	if k := p.PickWithHint("zz", now); k == nil {
		t.Fatal("未知 hint 应回退 Pick")
	}
	// hint 键冷却 → 退回 Pick 选中其他键
	p.Report(&Key{ID: "a"}, OutcomeRateLimited, now, time.Minute)
	if k := p.PickWithHint("a", now.Add(time.Second)); k == nil || k.ID == "a" {
		t.Fatalf("冷却中的 hint 键必须让位: %+v", k)
	}
}

// TestPickWithHint_不耗WRR记账 评审 C4 哨兵:等权池 hint 直取后,下一次 Pick 仍应回到
// hint 键(smooth WRR 记账未被亲和消耗——若 hint 动了 current,b 会先出)。
func TestPickWithHint_不耗WRR记账(t *testing.T) {
	p := NewPool([]KeyInput{{ID: "a", Value: "va", Weight: 1}, {ID: "b", Value: "vb", Weight: 1}})
	now := time.Unix(2_100_000_000, 0)
	for i := 0; i < 3; i++ {
		if k := p.PickWithHint("a", now); k == nil || k.ID != "a" {
			t.Fatalf("hint 直取应恒 a: %+v", k)
		}
	}
	if k := p.Pick(now); k == nil || k.ID != "a" {
		t.Fatalf("hint 不得消耗 WRR 记账(下一次 Pick 仍应 a): %+v", k)
	}
}
