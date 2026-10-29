package balance

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"time"
)

// 规则键形态:below 为绝对阈值规则,tier:<hours> 为剩余时间档位规则;
// 与 store.balance_alert_states.rule_key 及事件键共用同一形态。
const (
	RuleKeyBelow      = "below"
	RuleKeyTierPrefix = "tier:"
)

// TierRuleKey 档位规则键。
func TierRuleKey(hours int64) string { return RuleKeyTierPrefix + strconv.FormatInt(hours, 10) }

// Rule 告警规则:below(绝对阈值,余额币种 credit)或 tier(剩余小时档位)。
type Rule struct {
	Key          string
	BelowCredits *int64
	TierHours    int64
	IsTier       bool
}

// RuleBelow 构造绝对阈值规则。
func RuleBelow(below int64) Rule { return Rule{Key: RuleKeyBelow, BelowCredits: &below} }

// RuleTier 构造档位规则。
func RuleTier(hours int64) Rule {
	return Rule{Key: TierRuleKey(hours), TierHours: hours, IsTier: true}
}

// RulesFor 渠道规则集:档位表 + 可选绝对阈值(渠道未声明 below 时为 nil)。
func RulesFor(tierHours []int64, below *int64) []Rule {
	rules := make([]Rule, 0, len(tierHours)+1)
	for _, h := range tierHours {
		rules = append(rules, RuleTier(h))
	}
	if below != nil {
		rules = append(rules, RuleBelow(*below))
	}
	return rules
}

// SeverityForTier 档位严重级缺省映射:≥14 天 info,≥3 天 warning,其余 critical。
func SeverityForTier(hours int64) string {
	switch {
	case hours >= 336:
		return "info"
	case hours >= 72:
		return "warning"
	default:
		return "critical"
	}
}

// RuleState 单规则持久化状态(与 store.AlertState 同构)。
type RuleState struct {
	Firing      bool
	Severity    string
	LastFiredAt *time.Time
	ContextJSON string
}

// Firing 一次评估产生的发射指令;服务层转 Notifier.Emit(上下文并入载荷)。
type Firing struct {
	RuleKey  string
	Severity string
	Summary  string
	Context  map[string]any
	At       time.Time
}

// EvaluateInput 评估输入;Prev 来自持久化状态(重启恢复不重复告警的关键)。
type EvaluateInput struct {
	Provider   string
	Rules      []Rule
	Prev       map[string]RuleState
	Est        Estimate
	BelowValue *int64 // below 规则判据:池内最小有效余额(币种 = Est.PrimaryCurrency)
	RearmMilli int64  // 迟滞回差毫单位,如 1200 = 1.2 倍
	Now        time.Time
}

// Evaluate 告警状态机(纯函数):
//
//	armed → firing:跌破触发线(below:余额 < 阈值;tier:剩余 ≤ 档位),发射一次事件
//	firing → armed:回升超 触发线 × 迟滞系数,静默解除(不发事件)
//	判据不可得(余额不可知/无估算)时状态保持原样——既不误触发也不误解除
//
// 返回全量新状态(含未变化项,服务层整表 upsert)与发射清单。
func Evaluate(in EvaluateInput) (map[string]RuleState, []Firing) {
	out := make(map[string]RuleState, len(in.Prev)+len(in.Rules))
	for k, st := range in.Prev {
		out[k] = st
	}
	var firings []Firing
	for _, r := range in.Rules {
		st := out[r.Key]
		switch {
		case r.IsTier:
			if in.Est.RemainingHours == nil {
				continue // 判据不可得:状态保持
			}
			hours := *in.Est.RemainingHours
			if !st.Firing {
				if hours <= r.TierHours {
					summary := fmt.Sprintf("渠道 %s 余额已透支,已进入 %d 小时档告警",
						in.Provider, r.TierHours)
					if hours >= 0 {
						summary = fmt.Sprintf("渠道 %s 余额约可支撑 %s,已进入 %d 小时档告警",
							in.Provider, HumanRemaining(hours), r.TierHours)
					}
					f := newFiring(in, r.Key, SeverityForTier(r.TierHours), summary)
					firings = append(firings, f)
					st.Firing, st.Severity, st.LastFiredAt, st.ContextJSON = true, f.Severity, &f.At, marshalContext(f)
				}
			} else if hours > scaledLine(r.TierHours, in.RearmMilli) {
				st.Firing, st.ContextJSON = false, "" // 迟滞解除,重新武装
			}
		default: // below
			if in.BelowValue == nil || r.BelowCredits == nil {
				continue
			}
			value, threshold := *in.BelowValue, *r.BelowCredits
			if !st.Firing {
				if value < threshold {
					f := newFiring(in, r.Key, "critical",
						fmt.Sprintf("渠道 %s 有效余额 %s %s 已低于告警阈值 %s",
							in.Provider, FormatCredits(value), in.Est.PrimaryCurrency, FormatCredits(threshold)))
					firings = append(firings, f)
					st.Firing, st.Severity, st.LastFiredAt, st.ContextJSON = true, f.Severity, &f.At, marshalContext(f)
				}
			} else if value >= scaledLine(threshold, in.RearmMilli) {
				st.Firing, st.ContextJSON = false, ""
			}
		}
		out[r.Key] = st
	}
	return out, firings
}

// newFiring 组装发射指令与上下文(渠道名 + 余额/估算,余额观测事件载荷契约)。
func newFiring(in EvaluateInput, ruleKey, severity, summary string) Firing {
	ctx := map[string]any{"provider": in.Provider, "rule": ruleKey}
	if in.Est.MinEffective != nil {
		ctx["balance"] = FormatCredits(*in.Est.MinEffective)
		ctx["currency"] = in.Est.PrimaryCurrency
	}
	if in.Est.RemainingHours != nil {
		ctx["remaining_hours"] = *in.Est.RemainingHours
	}
	return Firing{RuleKey: ruleKey, Severity: severity, Summary: summary, Context: ctx, At: in.Now}
}

func marshalContext(f Firing) string {
	b, err := json.Marshal(f.Context)
	if err != nil { // map[string]any 序列化不可失败;防御性兜底
		return ""
	}
	return string(b)
}

// scaledLine 迟滞解除线 = base × milli / 1000(整数运算);base 大到乘法越界时
// 零回差退化(该量级下阈值本身已达上界,解除线与触发线重合是唯一无溢出解)。
func scaledLine(base, milli int64) int64 {
	if base > math.MaxInt64/milli {
		return base
	}
	return base * milli / 1000
}
