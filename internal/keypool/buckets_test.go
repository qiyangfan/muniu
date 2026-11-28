package keypool

import (
	"testing"
	"time"
)

// 冷却桶隔离测试:credit/rate 独立计时、充值只释放 credit、按桶持久化。

type recordSink struct {
	recs []CooldownRecord
}

func (s *recordSink) SaveCooldown(_, keyID, bucket string, until time.Time, consec int, seq int64) error {
	s.recs = append(s.recs, CooldownRecord{KeyID: keyID, Bucket: bucket, Until: until, Consec: consec, Seq: seq})
	return nil
}

func TestCooldownBuckets_独立计时与释放(t *testing.T) {
	now := time.Now()
	p := NewPool([]KeyInput{{ID: "k1", Value: "v1"}})
	sink := &recordSink{}
	p.WithSink("p", sink)

	p.Report(&Key{ID: "k1"}, OutcomeCreditExhausted, now, 0)
	p.Report(&Key{ID: "k1"}, OutcomeRateLimited, now, 2*time.Hour)
	if p.StateOf("k1", now) != "cooldown" {
		t.Fatal("双桶并存应不可用")
	}
	// 充值只释放 credit;rate 桶仍生效
	p.RecordCreditExhausted("k1", false, now)
	if p.StateOf("k1", now) != "cooldown" {
		t.Fatal("rate 桶不得被充值释放")
	}
	if p.CooldownRemain("k1", now) < time.Hour {
		t.Fatalf("剩余冷却应来自 rate 桶,实际 %v", p.CooldownRemain("k1", now))
	}
	// 桶级持久化事件:credit 进入、rate 进入、credit 清除
	var creditEnter, rateEnter, creditClear bool
	for _, r := range sink.recs {
		switch {
		case r.Bucket == "credit" && !r.Until.IsZero():
			creditEnter = true
		case r.Bucket == "rate" && !r.Until.IsZero():
			rateEnter = true
		case r.Bucket == "credit" && r.Until.IsZero():
			creditClear = true
		}
	}
	if !creditEnter || !rateEnter || !creditClear {
		t.Fatalf("桶级事件缺失: %+v", sink.recs)
	}
}

func TestCooldownBuckets_重启恢复双桶(t *testing.T) {
	now := time.Now()
	p := NewPool([]KeyInput{{ID: "k1", Value: "v1"}})
	p.LoadCooldowns([]CooldownRecord{
		{KeyID: "k1", Bucket: "credit", Until: now.Add(30 * time.Minute), Consec: 1, Seq: 1},
		{KeyID: "k1", Bucket: "rate", Until: now.Add(2 * time.Hour), Consec: 2, Seq: 2},
	}, now)
	if p.StateOf("k1", now) != "cooldown" {
		t.Fatal("双桶恢复后应不可用")
	}
	if p.Pick(now) != nil {
		t.Fatal("双桶未到期不得选中")
	}
	if p.Pick(now.Add(3*time.Hour)) == nil {
		t.Fatal("双桶到期后应恢复可选")
	}
}

// 成功路径清除已到期桶的持久行,避免长期积累;未到期桶不得被成功提前解除。
func TestCooldownBuckets_成功清理已到期桶(t *testing.T) {
	now := time.Now()
	p := NewPool([]KeyInput{{ID: "k1", Value: "v1"}})
	sink := &recordSink{}
	p.WithSink("p", sink)

	p.Report(&Key{ID: "k1"}, OutcomeCreditExhausted, now, 0)
	p.Report(&Key{ID: "k1"}, OutcomeRateLimited, now, 2*time.Hour)
	p.Report(&Key{ID: "k1"}, OutcomeSuccess, now.Add(3*time.Hour), 0)

	if p.CooldownRemain("k1", now.Add(3*time.Hour)) != 0 {
		t.Fatal("已到期桶应被成功路径清除")
	}
	var creditClear, rateClear bool
	for _, r := range sink.recs {
		if r.Until.IsZero() {
			switch r.Bucket {
			case "credit":
				creditClear = true
			case "rate":
				rateClear = true
			}
		}
	}
	if !creditClear || !rateClear {
		t.Fatalf("已到期 rate/credit 均应产生持久清除事件: %+v", sink.recs)
	}
}

func TestCooldownBuckets_成功不解除未到期桶(t *testing.T) {
	now := time.Now()
	p := NewPool([]KeyInput{{ID: "k1", Value: "v1"}})
	sink := &recordSink{}
	p.WithSink("p", sink)

	p.Report(&Key{ID: "k1"}, OutcomeCreditExhausted, now, 0)
	p.Report(&Key{ID: "k1"}, OutcomeSuccess, now.Add(time.Minute), 0)
	if p.StateOf("k1", now.Add(time.Minute)) != "cooldown" {
		t.Fatal("未到期 credit 桶不得被成功提前解除")
	}
	for _, r := range sink.recs {
		if r.Bucket == "credit" && r.Until.IsZero() {
			t.Fatalf("未到期桶不得产生清除事件: %+v", sink.recs)
		}
	}
}
