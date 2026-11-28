package keypool

import (
	"testing"
	"time"
)

// TestProbeKeys_只返回可服务键 探针取样必须跳过禁用与冷却 key,且不改变任何调度状态。
func TestProbeKeys_只返回可服务键(t *testing.T) {
	now := time.Now()
	p := NewPool([]KeyInput{{ID: "k1", Value: "v1"}, {ID: "k2", Value: "v2"}, {ID: "k3", Value: "v3"}})
	p.Report(&Key{ID: "k1"}, OutcomeRateLimited, now, time.Hour)
	p.Report(&Key{ID: "k2"}, OutcomeAuthFailed, now, 0)
	before1, before2 := p.EMARate("k1"), p.EMARate("k2")
	got := p.ProbeKeys(now)
	if len(got) != 1 || got[0].ID != "k3" || got[0].Value != "v3" {
		t.Fatalf("探针应只取健康键 k3,实际 %+v", got)
	}
	if p.EMARate("k1") != before1 || p.EMARate("k2") != before2 {
		t.Fatal("ProbeKeys 不得改变 EMA")
	}
	if p.StateOf("k1", now) != "cooldown" || p.StateOf("k2", now) != "disabled" {
		t.Fatal("ProbeKeys 不得改变冷却/禁用状态")
	}
}

// TestProbeKeys_全部不可用 全冷却/禁用时返回空集,由 health 跳过探针而非计失败。
func TestProbeKeys_全部不可用(t *testing.T) {
	now := time.Now()
	p := NewPool([]KeyInput{{ID: "k1", Value: "v1"}})
	p.Report(&Key{ID: "k1"}, OutcomeAuthFailed, now, 0)
	if got := p.ProbeKeys(now); len(got) != 0 {
		t.Fatalf("全不可用应返回空集,实际 %+v", got)
	}
}

func TestAvailability_空池不误判限频(t *testing.T) {
	av := NewPool(nil).Availability(time.Now())
	if av.Available || av.AllRateLimited {
		t.Fatalf("空池应为不可用且非限频: %+v", av)
	}
}
