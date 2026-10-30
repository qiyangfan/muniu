// Package health 渠道级上游健康观测与熔断降级:免费控制面探针(缺省全开)、付费推理探针
// (声明制)与被动统计(真实流量换道/失败)融合为统一状态机;降级渠道被候选域收缩
// (多候选透明切换、单候选快速失败),恢复判据与降级原因同层对称。
// 状态为进程内存态:健康是短生命周期信号,重启重置并由首轮探针立即校准(设计 D4)。
package health

import (
	"fmt"
	"time"
)

// Phase 渠道健康相位。
type Phase uint8

const (
	// PhaseHealthy 正常参与路由。
	PhaseHealthy Phase = iota
	// PhaseDegraded 降级:候选域收缩(候选循环整路跳过)。
	PhaseDegraded
	// PhaseHalfOpen 试探:放行流量,下一真实请求或同层探针裁决恢复。
	PhaseHalfOpen
)

func (p Phase) String() string {
	switch p {
	case PhaseDegraded:
		return "degraded"
	case PhaseHalfOpen:
		return "half_open"
	default:
		return "healthy"
	}
}

// Reason 降级原因;恢复判据必须与原因同层(免费探针阴性由免费探针阳性翻案,
// 被动统计阴性只由真实流量翻案——probe_model 与失败模型可能不同 lane,跨层解除是不诚实恢复)。
type Reason uint8

const (
	// ReasonControlPlane 免费控制面探针阴性(网络/鉴权/服务不可用)。
	ReasonControlPlane Reason = iota
	// ReasonInference 付费推理探针阴性(控制面活、推理面坏)。
	ReasonInference
	// ReasonPassive 真实流量失败统计越线。
	ReasonPassive
)

func (r Reason) String() string {
	switch r {
	case ReasonInference:
		return "inference"
	case ReasonPassive:
		return "passive"
	default:
		return "control_plane"
	}
}

// ProbeOutcome 探针单次结果。
type ProbeOutcome uint8

const (
	// ProbeOK 探针 2xx。
	ProbeOK ProbeOutcome = iota
	// ProbeFail 确定失败(网络/超时/5xx/4xx)。
	ProbeFail
	// ProbeLimited 429:存活但受限,不计成败(防探针自身限频误降级活渠道)。
	ProbeLimited
)

// Event 状态迁移产生的运维事件(装配层转 alerting 管道)。
type Event struct {
	Kind     string // provider_degraded | provider_recovered
	Provider string
	Reason   string
	Summary  string
	At       time.Time
}

// ProviderState 单渠道健康态(Manager 持有;apply* 纯函数返回新值)。
type ProviderState struct {
	Provider       string
	Phase          Phase
	Reason         Reason // 降级原因(half-open 保持原因供同层裁决)
	FailStreak     int    // 当前连续失败计数(探针,任一层)
	OKStreak       int    // 当前连续成功计数(探针;同层恢复计数)
	DegradedAt     time.Time
	OpenUntil      time.Time // degraded 计时到期转 half-open
	RecoveredAt    time.Time
	LastProbeAt    time.Time
	LastProbeOK    bool
	HasProbe       bool      // 是否已有探针结果
	LastProbeClass string    // 最近探针错误归类(http_status_NNN/network/timeout)
	LastActiveAt   time.Time // 最近任一信号(探针/流量试探/被动统计/周期推进)触达时刻;长期无信号即 GC(防热替换移除的渠道幽灵永驻)
}

// Contracted 降级态是否收缩候选域:仅 Degraded 收缩,HalfOpen 放行流量作试探。
func (st ProviderState) Contracted() bool { return st.Phase == PhaseDegraded }

// applyProbe 探针结果推进状态机(免费/付费探针同用,layer 标记信号层)。
// 迁移:healthy + 连续失败达阈值 → degraded(该层原因);
// degraded + 同层连续成功达恢复阈值 → recovered(控制面层要求连续达阈值,推理面层单次成功即可,
// 对应 spec「免费探针连续成功恢复/付费探针成功恢复」);degraded + 同层失败 → 续期熔断计时;
// half-open + 同层探针成败 → recovered / 回 degraded。
func applyProbe(st ProviderState, outcome ProbeOutcome, class string, layer Reason, now time.Time,
	threshold int, openFor time.Duration) (ProviderState, *Event) {
	st.LastProbeAt = now
	st.LastActiveAt = now
	if outcome != ProbeLimited {
		st.HasProbe = true
		st.LastProbeOK = outcome == ProbeOK
		st.LastProbeClass = class
	}
	switch outcome {
	case ProbeLimited:
		return st, nil // 存活但受限:不计成败,维持原态
	case ProbeOK:
		st.OKStreak++
		st.FailStreak = 0
		switch st.Phase {
		case PhaseHealthy:
			return st, nil
		case PhaseDegraded:
			// 跨层成功不解除(被动/推理面阴性不因免费探针阳性翻案)
			if layer != st.Reason {
				return st, nil
			}
			if recoveryStreak(st.Reason, threshold) <= st.OKStreak {
				return exitDegraded(st, now), recoveredEvent(st, now)
			}
			return st, nil
		default: // half-open:同层探针成功即恢复
			if layer == st.Reason {
				return exitDegraded(st, now), recoveredEvent(st, now)
			}
			return st, nil
		}
	default: // ProbeFail
		st.OKStreak = 0
		st.FailStreak++
		switch st.Phase {
		case PhaseHealthy:
			if st.FailStreak >= threshold {
				return enterDegraded(st, layer, now, openFor), degradedEvent(st, now)
			}
			return st, nil
		case PhaseDegraded:
			if layer == st.Reason {
				st.OpenUntil = now.Add(openFor) // 同层继续阴性:续期熔断计时
			}
			return st, nil
		default: // half-open:试探失败回降级
			return enterDegraded(st, st.Reason, now, openFor), nil
		}
	}
}

// applyTick 周期推进:degraded 计时到期转 half-open(无事件);周期触达同时充当活跃信号。
func applyTick(st ProviderState, now time.Time) ProviderState {
	st.LastActiveAt = now
	if st.Phase == PhaseDegraded && !st.OpenUntil.IsZero() && !now.Before(st.OpenUntil) {
		st.Phase = PhaseHalfOpen
	}
	return st
}

// applyTrafficTrial 半开期真实请求试探裁决(Manager 请求侧回填;非半开态 no-op)。
// 真实流量成功是全层证据(它同时证明控制面与推理面),对任意降级原因均可恢复。
func applyTrafficTrial(st ProviderState, ok bool, now time.Time, openFor time.Duration) (ProviderState, *Event) {
	st.LastActiveAt = now
	if st.Phase != PhaseHalfOpen {
		return st, nil
	}
	if ok {
		return exitDegraded(st, now), recoveredEvent(st, now)
	}
	return enterDegraded(st, st.Reason, now, openFor), nil
}

// applyPassive 被动统计越线推进(真实流量失败率窗口判定回填)。
// 越线仅在 healthy 态触发降级(被动原因);degraded/half-open 态的越线只续期;
// 越线解除不主动恢复——被动降级只由真实流量试探恢复(同层纪律)。
func applyPassive(st ProviderState, breached bool, now time.Time, openFor time.Duration) (ProviderState, *Event) {
	st.LastActiveAt = now
	if !breached {
		return st, nil
	}
	switch st.Phase {
	case PhaseHealthy:
		ns := enterDegraded(st, ReasonPassive, now, openFor)
		return ns, degradedEvent(ns, now)
	case PhaseDegraded:
		if st.Reason == ReasonPassive {
			st.OpenUntil = now.Add(openFor)
		}
		return st, nil
	default:
		return st, nil
	}
}

func enterDegraded(st ProviderState, r Reason, now time.Time, openFor time.Duration) ProviderState {
	st.Phase = PhaseDegraded
	st.Reason = r
	st.DegradedAt = now
	st.OpenUntil = now.Add(openFor)
	st.FailStreak = 0
	st.OKStreak = 0
	return st
}

func exitDegraded(st ProviderState, now time.Time) ProviderState {
	st.Phase = PhaseHealthy
	st.Reason = 0
	st.OpenUntil = time.Time{}
	st.RecoveredAt = now
	st.FailStreak = 0
	st.OKStreak = 0
	return st
}

// recoveryStreak 同层探针恢复所需连续成功数:控制面层与失败阈值对称(确定性信号,
// 连续成功翻案才可信),推理面层单次成功即可(spec 口径「付费探针成功恢复」)。
func recoveryStreak(r Reason, threshold int) int {
	if r == ReasonInference {
		return 1
	}
	if threshold < 1 {
		return 1
	}
	return threshold
}

func degradedEvent(st ProviderState, now time.Time) *Event {
	return &Event{
		Kind: "provider_degraded", Provider: st.Provider, Reason: st.Reason.String(), At: now,
		Summary: fmt.Sprintf("上游渠道 %s 进入健康降级(原因 %s),已收缩出候选域;熔断 %s 后转试探",
			st.Provider, st.Reason, st.OpenUntil.Sub(st.DegradedAt)),
	}
}

func recoveredEvent(st ProviderState, now time.Time) *Event {
	dur := ""
	if !st.DegradedAt.IsZero() {
		dur = fmt.Sprintf(",降级历时 %s", now.Sub(st.DegradedAt).Round(time.Second))
	}
	return &Event{
		Kind: "provider_recovered", Provider: st.Provider, At: now,
		Summary: fmt.Sprintf("上游渠道 %s 健康恢复,重新参与路由%s", st.Provider, dur),
	}
}
