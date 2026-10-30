// 健康状态机纯函数契约:阈值降级、429 维持原态、同层恢复守卫、open 到期转半开、
// 半开试探分叉、被动越线降级与续期。
package health

import (
	"testing"
	"time"
)

var at = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

func healthy() ProviderState { return ProviderState{Provider: "p", Phase: PhaseHealthy} }

func TestApplyProbe_连续失败达阈值降级(t *testing.T) {
	st := healthy()
	st, _ = applyProbe(st, ProbeFail, "http_status_503", ReasonControlPlane, at, 2, 5*time.Minute)
	if st.Phase != PhaseHealthy || st.FailStreak != 1 {
		t.Fatalf("首败不得降级: %+v", st)
	}
	st2, ev := applyProbe(st, ProbeFail, "http_status_503", ReasonControlPlane, at.Add(time.Minute), 2, 5*time.Minute)
	if st2.Phase != PhaseDegraded || st2.Reason != ReasonControlPlane {
		t.Fatalf("连续两败应降级: %+v", st2)
	}
	if ev == nil || ev.Kind != "provider_degraded" || ev.Reason != "control_plane" {
		t.Fatalf("降级事件缺失: %+v", ev)
	}
	if !st2.Contracted() {
		t.Fatalf("降级态应收缩候选域")
	}
}

func TestApplyProbe_429维持原态(t *testing.T) {
	st := healthy()
	st, _ = applyProbe(st, ProbeFail, "http_status_503", ReasonControlPlane, at, 2, 5*time.Minute)
	st2, ev := applyProbe(st, ProbeLimited, "http_status_429", ReasonControlPlane, at.Add(time.Minute), 2, 5*time.Minute)
	if ev != nil || st2.FailStreak != 1 || st2.Phase != PhaseHealthy {
		t.Fatalf("429 应不计成败维持原态: %+v ev=%v", st2, ev)
	}
}

func TestApplyProbe_同层恢复守卫(t *testing.T) {
	// 被动降级后探针成功(跨层):不得解除
	st := enterDegraded(healthy(), ReasonPassive, at, 5*time.Minute)
	var ev *Event
	st, ev = applyProbe(st, ProbeOK, "", ReasonControlPlane, at.Add(time.Minute), 2, 5*time.Minute)
	if ev != nil || st.Phase != PhaseDegraded {
		t.Fatalf("跨层探针成功不得解除被动降级: %+v ev=%v", st, ev)
	}
	// 推理面降级 + 付费探针单次成功:恢复
	st = enterDegraded(healthy(), ReasonInference, at, 5*time.Minute)
	st, ev = applyProbe(st, ProbeOK, "", ReasonInference, at.Add(time.Minute), 2, 5*time.Minute)
	if ev == nil || ev.Kind != "provider_recovered" || st.Phase != PhaseHealthy {
		t.Fatalf("推理面单次成功应恢复: %+v ev=%v", st, ev)
	}
	// 控制面降级 + 免费探针单次成功:不恢复(须连续达阈值),连续达阈值后恢复
	st = enterDegraded(healthy(), ReasonControlPlane, at, 5*time.Minute)
	st, ev = applyProbe(st, ProbeOK, "", ReasonControlPlane, at.Add(time.Minute), 2, 5*time.Minute)
	if ev != nil || st.Phase != PhaseDegraded {
		t.Fatalf("控制面单次成功不应恢复: %+v ev=%v", st, ev)
	}
	st, ev = applyProbe(st, ProbeOK, "", ReasonControlPlane, at.Add(2*time.Minute), 2, 5*time.Minute)
	if ev == nil || st.Phase != PhaseHealthy {
		t.Fatalf("控制面连续成功应恢复: %+v ev=%v", st, ev)
	}
}

func TestApplyTick_open到期转半开(t *testing.T) {
	st := enterDegraded(healthy(), ReasonControlPlane, at, 5*time.Minute)
	st2 := applyTick(st, at.Add(4*time.Minute))
	if st2.Phase != PhaseDegraded {
		t.Fatalf("未到期不得转半开: %+v", st2)
	}
	st3 := applyTick(st, at.Add(5*time.Minute))
	if st3.Phase != PhaseHalfOpen || st3.Contracted() {
		t.Fatalf("到期应转半开且放行试探: %+v", st3)
	}
}

func TestApplyTrafficTrial_半开试探分叉(t *testing.T) {
	st := applyTick(enterDegraded(healthy(), ReasonPassive, at, 5*time.Minute), at.Add(5*time.Minute))
	ok, ev := applyTrafficTrial(st, true, at.Add(6*time.Minute), 5*time.Minute)
	if ev == nil || ev.Kind != "provider_recovered" || ok.Phase != PhaseHealthy {
		t.Fatalf("半开试探成功应恢复: %+v ev=%v", ok, ev)
	}
	fail, ev2 := applyTrafficTrial(st, false, at.Add(6*time.Minute), 5*time.Minute)
	if ev2 != nil || fail.Phase != PhaseDegraded {
		t.Fatalf("半开试探失败应回降级: %+v ev=%v", fail, ev2)
	}
	// 非半开态 no-op
	h := healthy()
	h2, ev3 := applyTrafficTrial(h, true, at, 5*time.Minute)
	if ev3 != nil || h2.Phase != PhaseHealthy {
		t.Fatalf("非半开试探应 no-op: %+v ev=%v", h2, ev3)
	}
}

func TestApplyPassive_越线降级与同层续期(t *testing.T) {
	st, ev := applyPassive(healthy(), true, at, 5*time.Minute)
	if ev == nil || st.Phase != PhaseDegraded || st.Reason != ReasonPassive {
		t.Fatalf("被动越线应降级: %+v ev=%v", st, ev)
	}
	// 被动降级继续越线:续期不重复事件
	_, ev2 := applyPassive(st, true, at.Add(time.Minute), 5*time.Minute)
	if ev2 != nil {
		t.Fatalf("已降级再越线不得重复事件: %+v", ev2)
	}
	// 越线解除:不主动恢复(被动只由真实流量试探恢复)
	st3, ev3 := applyPassive(st, false, at.Add(2*time.Minute), 5*time.Minute)
	if ev3 != nil || st3.Phase != PhaseDegraded {
		t.Fatalf("越线解除不得恢复: %+v ev=%v", st3, ev3)
	}
	// 推理面降级遇被动越线:不续期不迁移
	inf := enterDegraded(healthy(), ReasonInference, at, 5*time.Minute)
	inf2, ev4 := applyPassive(inf, true, at.Add(time.Minute), 5*time.Minute)
	if ev4 != nil || inf2.OpenUntil != inf.OpenUntil {
		t.Fatalf("跨层被动越线不得续期: %+v", inf2)
	}
}

func TestApplyProbe_半开同层探针分叉(t *testing.T) {
	st := applyTick(enterDegraded(healthy(), ReasonControlPlane, at, 5*time.Minute), at.Add(5*time.Minute))
	// 同层失败:回降级
	fail, _ := applyProbe(st, ProbeFail, "http_status_503", ReasonControlPlane, at.Add(6*time.Minute), 2, 5*time.Minute)
	if fail.Phase != PhaseDegraded {
		t.Fatalf("半开探针失败应回降级: %+v", fail)
	}
	// 同层成功:恢复
	okSt, ev := applyProbe(st, ProbeOK, "", ReasonControlPlane, at.Add(6*time.Minute), 2, 5*time.Minute)
	if ev == nil || okSt.Phase != PhaseHealthy {
		t.Fatalf("半开同层探针成功应恢复: %+v ev=%v", okSt, ev)
	}
	// 跨层成功:不解除(维持半开等待同层证据)
	cross, ev2 := applyProbe(st, ProbeOK, "", ReasonInference, at.Add(6*time.Minute), 2, 5*time.Minute)
	if ev2 != nil || cross.Phase != PhaseHalfOpen {
		t.Fatalf("半开跨层探针成功不得解除: %+v ev=%v", cross, ev2)
	}
}
