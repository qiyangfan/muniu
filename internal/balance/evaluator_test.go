package balance

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func evalIn(rules []Rule, prev map[string]RuleState, est Estimate, below *int64) EvaluateInput {
	return EvaluateInput{
		Provider: "deepseek", Rules: rules, Prev: prev, Est: est,
		BelowValue: below, RearmMilli: 1200, Now: time.Now(),
	}
}

func testEst(hours *int64, minEff *int64) Estimate {
	return Estimate{RemainingHours: hours, MinEffective: minEff, PrimaryCurrency: "CNY"}
}

func TestEvaluate_档位穿越单次发射(t *testing.T) {
	rules := []Rule{RuleTier(72)}
	states, firings := Evaluate(evalIn(rules, nil, testEst(ptrInt(58), ptrInt(1_000_000_000)), nil))
	if len(firings) != 1 || firings[0].RuleKey != "tier:72" || firings[0].Severity != "warning" {
		t.Fatalf("穿越应发射一次 warning: %+v", firings)
	}
	if !states["tier:72"].Firing {
		t.Fatalf("状态应转为 firing")
	}
	// 二次评估仍在触发区内:不重复发射
	states2, firings2 := Evaluate(evalIn(rules, states, testEst(ptrInt(50), ptrInt(1_000_000_000)), nil))
	if len(firings2) != 0 || !states2["tier:72"].Firing {
		t.Fatalf("同档重复评估不得再发射: %+v", firings2)
	}
}

func TestEvaluate_迟滞解除与重新武装(t *testing.T) {
	rules := []Rule{RuleTier(72)}
	states, _ := Evaluate(evalIn(rules, nil, testEst(ptrInt(58), nil), nil))
	if !states["tier:72"].Firing {
		t.Fatalf("前置 firing 态未建立")
	}
	// 回升到 80(未超 72×1.2=86.4):保持 firing
	states, firings := Evaluate(evalIn(rules, states, testEst(ptrInt(80), nil), nil))
	if len(firings) != 0 || !states["tier:72"].Firing {
		t.Fatalf("迟滞区内不得解除: %+v", firings)
	}
	// 回升到 90(>86.4):解除
	states, _ = Evaluate(evalIn(rules, states, testEst(ptrInt(90), nil), nil))
	if states["tier:72"].Firing {
		t.Fatalf("超迟滞线应解除")
	}
	// 再跌破:重新武装并再次发射
	_, firings = Evaluate(evalIn(rules, states, testEst(ptrInt(60), nil), nil))
	if len(firings) != 1 {
		t.Fatalf("解除后再穿越应重新发射: %+v", firings)
	}
}

func TestEvaluate_多档直落逐档发射(t *testing.T) {
	rules := []Rule{RuleTier(168), RuleTier(72), RuleTier(24)}
	states, firings := Evaluate(evalIn(rules, nil, testEst(ptrInt(10), nil), nil))
	if len(firings) != 3 {
		t.Fatalf("直落三档应各发一次: %+v", firings)
	}
	if firings[0].Severity != "warning" || firings[1].Severity != "warning" || firings[2].Severity != "critical" {
		t.Fatalf("严重级映射失真: %v %v %v", firings[0].Severity, firings[1].Severity, firings[2].Severity)
	}
	for _, k := range []string{"tier:168", "tier:72", "tier:24"} {
		if !states[k].Firing {
			t.Fatalf("%s 应处于 firing", k)
		}
	}
	// 30 天/14 天档为 info
	states2, f2 := Evaluate(evalIn([]Rule{RuleTier(720), RuleTier(336)}, nil, testEst(ptrInt(100), nil), nil))
	if len(f2) != 2 || f2[0].Severity != "info" || f2[1].Severity != "info" {
		t.Fatalf("长档位应为 info: %+v %+v", f2, states2)
	}
}

func TestEvaluate_below独立触发与解除(t *testing.T) {
	rules := []Rule{RuleTier(24), RuleBelow(10_000_000_000)}
	// 无估算(零消耗)但余额低于阈值:below 照常 critical
	below := ptrInt(8_000_000_000)
	states, firings := Evaluate(evalIn(rules, nil, testEst(nil, below), below))
	var belowFired bool
	for _, f := range firings {
		if f.RuleKey == RuleKeyBelow {
			belowFired = true
			if f.Severity != "critical" {
				t.Fatalf("below 应为 critical: %+v", f)
			}
			if !strings.Contains(f.Summary, "8") {
				t.Fatalf("摘要应携带余额: %q", f.Summary)
			}
		}
	}
	if !belowFired {
		t.Fatalf("below 应独立触发: %+v", firings)
	}
	if !states[RuleKeyBelow].Firing {
		t.Fatalf("below 状态失真")
	}
	// 回升超阈值×1.2:解除
	recovered := ptrInt(12_000_000_000)
	states, firings = Evaluate(evalIn(rules, states, testEst(ptrInt(100), recovered), recovered))
	for _, f := range firings {
		if f.RuleKey == RuleKeyBelow {
			t.Fatalf("解除不得发事件: %+v", f)
		}
	}
	if states[RuleKeyBelow].Firing {
		t.Fatalf("below 应解除")
	}
}

func TestEvaluate_重启恢复不重复与判据不可得保持(t *testing.T) {
	rules := []Rule{RuleTier(72)}
	prevFiring := map[string]RuleState{"tier:72": {Firing: true, Severity: "warning"}}
	// 重启后判据仍在触发区内:不重复发射,状态保持
	states, firings := Evaluate(evalIn(rules, prevFiring, testEst(ptrInt(50), nil), nil))
	if len(firings) != 0 || !states["tier:72"].Firing {
		t.Fatalf("重启恢复不得重复告警: %+v", firings)
	}
	// 判据不可得:firing 保持(不误解除),armed 保持(不误触发)
	estNoEst := Estimate{Reason: ReasonNoUsage}
	states, _ = Evaluate(evalIn(rules, prevFiring, estNoEst, nil))
	if !states["tier:72"].Firing {
		t.Fatalf("判据不可得不得解除既有 firing")
	}
	states, _ = Evaluate(evalIn(rules, nil, estNoEst, nil))
	if states["tier:72"].Firing {
		t.Fatalf("判据不可得不得凭空触发")
	}
	// below 同理:余额不可知时保持
	brules := []Rule{RuleBelow(10)}
	states, firings = Evaluate(evalIn(brules, nil, testEst(nil, nil), nil))
	if len(firings) != 0 || states[RuleKeyBelow].Firing {
		t.Fatalf("余额不可知不得触发 below: %+v", firings)
	}
}

func TestEvaluate_载荷上下文完整(t *testing.T) {
	rules := []Rule{RuleTier(24)}
	minEff := ptrInt(500_000_000)
	states, firings := Evaluate(evalIn(rules, nil, testEst(ptrInt(10), minEff), nil))
	if len(firings) != 1 {
		t.Fatalf("前置穿越缺失")
	}
	f := firings[0]
	if f.Context["provider"] != "deepseek" || f.Context["rule"] != "tier:24" {
		t.Fatalf("上下文缺渠道/规则: %+v", f.Context)
	}
	if f.Context["balance"] != "0.5" || f.Context["currency"] != "CNY" {
		t.Fatalf("上下文余额失真: %+v", f.Context)
	}
	// 状态表 ContextJSON 与发射上下文一致(JSON 往返)
	var decoded map[string]any
	if err := json.Unmarshal([]byte(states["tier:24"].ContextJSON), &decoded); err != nil {
		t.Fatalf("ContextJSON 非法: %v", err)
	}
	if decoded["provider"] != "deepseek" {
		t.Fatalf("ContextJSON 内容失真: %v", decoded)
	}
}

func TestEvaluate_透支档位摘要无语病(t *testing.T) {
	rules := []Rule{RuleTier(168)}
	// 负余额 + 正燃速 → 剩余小时为负:摘要不得出现「可支撑 已透支」语病
	states, firings := Evaluate(evalIn(rules, nil, testEst(ptrInt(-5), ptrInt(-3_000_000_000)), nil))
	if len(firings) != 1 {
		t.Fatalf("透支应触发档位: %+v", firings)
	}
	if strings.Contains(firings[0].Summary, "可支撑") || !strings.Contains(firings[0].Summary, "已透支") {
		t.Fatalf("透支摘要语病: %q", firings[0].Summary)
	}
	if !states["tier:168"].Firing {
		t.Fatalf("状态失真")
	}
}

// ---- 燃速突变(burn_spike)与余额骤降(cliff)规则 ----

func spikeIn(rules []Rule, prev map[string]RuleState, ratioMilli *int64) EvaluateInput {
	in := evalIn(rules, prev, Estimate{}, nil)
	in.SpikeRatioMilli = ratioMilli
	in.PrevBurnPerDay = 1_500_000_000 // 1.5 CNY/天
	in.CurBurnPerDay = 10_000_000_000 // 10 CNY/天(比值 ≈6.67 倍)
	return in
}

func TestEvaluate_燃速突变触发单次与基线不足(t *testing.T) {
	rules := []Rule{RuleBurnSpike(5000)}
	// 基线不足(判据不可得):不判定
	states, firings := Evaluate(spikeIn(rules, nil, nil))
	if len(firings) != 0 || states["burn_spike"].Firing {
		t.Fatalf("判据不可得不得触发: %+v", firings)
	}
	// 比值 6667 ≥ 5000:触发一次 warning,载荷含两窗口日均
	ratio := int64(6667)
	states, firings = Evaluate(spikeIn(rules, states, &ratio))
	if len(firings) != 1 || firings[0].RuleKey != "burn_spike" || firings[0].Severity != "warning" {
		t.Fatalf("突变应触发一次 warning: %+v", firings)
	}
	if firings[0].Context["prev_burn_per_day_credits"] != int64(1_500_000_000) ||
		firings[0].Context["cur_burn_per_day_credits"] != int64(10_000_000_000) {
		t.Fatalf("载荷缺两窗口日均: %+v", firings[0].Context)
	}
	// 持续越线:不重复发射
	states2, firings2 := Evaluate(spikeIn(rules, states, &ratio))
	if len(firings2) != 0 || !states2["burn_spike"].Firing {
		t.Fatalf("持续越线不得重复发射: %+v", firings2)
	}
}

func TestEvaluate_燃速突变迟滞解除与重武装(t *testing.T) {
	rules := []Rule{RuleBurnSpike(5000)}
	ratio := int64(6000)
	states, _ := Evaluate(spikeIn(rules, nil, &ratio))
	if !states["burn_spike"].Firing {
		t.Fatalf("前置 firing 未建立")
	}
	// 迟滞区内(4300 ≥ 5000÷1.2≈4166):保持 firing
	r2 := int64(4300)
	states, firings := Evaluate(spikeIn(rules, states, &r2))
	if len(firings) != 0 || !states["burn_spike"].Firing {
		t.Fatalf("迟滞区内不得解除: %+v", firings)
	}
	// 回落 4000(<4166):解除
	r3 := int64(4000)
	states, _ = Evaluate(spikeIn(rules, states, &r3))
	if states["burn_spike"].Firing {
		t.Fatalf("回落超迟滞线应解除")
	}
	// 再次越线:重新武装发射
	_, firings = Evaluate(spikeIn(rules, states, &ratio))
	if len(firings) != 1 {
		t.Fatalf("解除后再越线应重新发射: %+v", firings)
	}
}

func cliffIn(rules []Rule, prev map[string]RuleState, tr *CliffTransition) EvaluateInput {
	in := evalIn(rules, prev, Estimate{}, nil)
	if tr != nil {
		in.CliffTransitions = map[string]CliffTransition{"k1": *tr}
	}
	return in
}

func testTr(prev, cur int64, sameCur bool) *CliffTransition {
	return &CliffTransition{KeyID: "k1", PrevCredits: prev, CurrCredits: cur, SameCurrency: sameCur, Currency: "CNY"}
}

func TestEvaluate_骤降触发与平稳重武装(t *testing.T) {
	rules := []Rule{RuleCliff("k1", 500)}
	// 无转移(键缺失):不判定
	states, firings := Evaluate(cliffIn(rules, nil, nil))
	if len(firings) != 0 || states["cliff:k1"].Firing {
		t.Fatalf("无转移不得触发: %+v", firings)
	}
	// 自然小跌幅(3%):不触发
	states, firings = Evaluate(cliffIn(rules, states, testTr(100_000_000_000, 97_000_000_000, true)))
	if len(firings) != 0 {
		t.Fatalf("自然消耗不得触发: %+v", firings)
	}
	// 骤降 60%:触发一次 warning,载荷含 key 与前后余额
	states, firings = Evaluate(cliffIn(rules, states, testTr(100_000_000_000, 40_000_000_000, true)))
	if len(firings) != 1 || firings[0].RuleKey != "cliff:k1" || firings[0].Severity != "warning" {
		t.Fatalf("骤降应触发一次 warning: %+v", firings)
	}
	if firings[0].Context["key_id"] != "k1" {
		t.Fatalf("载荷应含 key_id: %+v", firings[0].Context)
	}
	// 已触发态持续(无新转移/仍骤降):不重复发射
	states2, firings2 := Evaluate(cliffIn(rules, states, nil))
	if len(firings2) != 0 || !states2["cliff:k1"].Firing {
		t.Fatalf("已触发态不得重复发射: %+v", firings2)
	}
	// 平稳观测(跌幅 3%):解除重武装
	states3, _ := Evaluate(cliffIn(rules, states, testTr(100_000_000_000, 97_000_000_000, true)))
	if states3["cliff:k1"].Firing {
		t.Fatalf("平稳观测应解除")
	}
	// 再次骤降:重新发射
	_, firings = Evaluate(cliffIn(rules, states3, testTr(100_000_000_000, 40_000_000_000, true)))
	if len(firings) != 1 {
		t.Fatalf("重武装后再骤降应发射: %+v", firings)
	}
}

func TestEvaluate_骤降不判定形态(t *testing.T) {
	rules := []Rule{RuleCliff("k1", 500)}
	// 币种不一致:不判定
	_, firings := Evaluate(cliffIn(rules, nil, testTr(100, 40, false)))
	if len(firings) != 0 {
		t.Fatalf("币种不一致不得触发: %+v", firings)
	}
	// 前值非正(欠费负值/零):不判定
	_, firings = Evaluate(cliffIn(rules, nil, testTr(-5, -9, true)))
	if len(firings) != 0 {
		t.Fatalf("前值非正不得触发: %+v", firings)
	}
	// 余额上升(充值):不触发(armed 侧)
	_, firings = Evaluate(cliffIn(rules, nil, testTr(100, 200, true)))
	if len(firings) != 0 {
		t.Fatalf("余额上升不得触发: %+v", firings)
	}
	// firing 态遇余额上升(充值):平稳语义解除重武装
	states, _ := Evaluate(cliffIn(rules, nil, testTr(100, 40, true)))
	if !states["cliff:k1"].Firing {
		t.Fatalf("前置 firing 未建立")
	}
	states, _ = Evaluate(cliffIn(rules, states, testTr(40, 200, true)))
	if states["cliff:k1"].Firing {
		t.Fatalf("充值应视为平稳观测解除")
	}
}
