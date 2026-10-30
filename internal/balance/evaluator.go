package balance

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"time"
)

// 规则键形态:below 为绝对阈值规则,tier:<hours> 为剩余时间档位规则,burn_spike 为燃速突变
// 渠道级规则,cliff:<key_id> 为逐 key 骤降规则;与 store.balance_alert_states.rule_key 及事件键共用同一形态。
const (
	RuleKeyBelow       = "below"
	RuleKeyTierPrefix  = "tier:"
	RuleKeyBurnSpike   = "burn_spike"
	RuleKeyCliffPrefix = "cliff:"
)

// TierRuleKey 档位规则键。
func TierRuleKey(hours int64) string { return RuleKeyTierPrefix + strconv.FormatInt(hours, 10) }

// CliffRuleKey 逐 key 骤降规则键。
func CliffRuleKey(keyID string) string { return RuleKeyCliffPrefix + keyID }

// RuleKind 告警规则种类。
type RuleKind uint8

const (
	// RuleKindBelow 绝对阈值规则。
	RuleKindBelow RuleKind = iota
	// RuleKindTier 剩余时间档位规则。
	RuleKindTier
	// RuleKindBurnSpike 燃速突变规则(渠道级)。
	RuleKindBurnSpike
	// RuleKindCliff 逐 key 余额骤降规则。
	RuleKindCliff
)

// Rule 告警规则:按 Kind 消费对应判据字段(below/tier 用余额与档位,burn_spike 用比值触发线,
// cliff 用跌幅触发线与归属 key)。
type Rule struct {
	Key             string
	Kind            RuleKind
	BelowCredits    *int64 // RuleKindBelow:绝对阈值(余额币种 credit)
	TierHours       int64  // RuleKindTier:剩余小时档位
	SpikeRatioMilli int64  // RuleKindBurnSpike:触发线(当前/上一窗口日均比值,毫单位)
	CliffRatioMilli int64  // RuleKindCliff:触发线(相邻快照跌幅,毫单位)
	CliffKeyID      string // RuleKindCliff:归属 key
}

// IsTier 兼容判别(档位规则分支)。
func (r Rule) IsTier() bool { return r.Kind == RuleKindTier }

// RuleBelow 构造绝对阈值规则。
func RuleBelow(below int64) Rule {
	return Rule{Key: RuleKeyBelow, Kind: RuleKindBelow, BelowCredits: &below}
}

// RuleTier 构造档位规则。
func RuleTier(hours int64) Rule {
	return Rule{Key: TierRuleKey(hours), Kind: RuleKindTier, TierHours: hours}
}

// RuleBurnSpike 构造燃速突变规则(ratioMilli=触发线毫单位)。
func RuleBurnSpike(ratioMilli int64) Rule {
	return Rule{Key: RuleKeyBurnSpike, Kind: RuleKindBurnSpike, SpikeRatioMilli: ratioMilli}
}

// RuleCliff 构造逐 key 骤降规则(dropRatioMilli=触发线毫单位)。
func RuleCliff(keyID string, dropRatioMilli int64) Rule {
	return Rule{Key: CliffRuleKey(keyID), Kind: RuleKindCliff, CliffRatioMilli: dropRatioMilli, CliffKeyID: keyID}
}

// RulesFor 渠道规则集:档位表 + 可选绝对阈值 + 可选燃速突变(ratioMilli≤0 省略 = 禁用)
// + 逐 key 骤降(cliffKeys 非空且 dropRatioMilli>0)。
func RulesFor(tierHours []int64, below *int64, spikeRatioMilli int64, cliffKeys []string, dropRatioMilli int64) []Rule {
	rules := make([]Rule, 0, len(tierHours)+1+len(cliffKeys)+1)
	for _, h := range tierHours {
		rules = append(rules, RuleTier(h))
	}
	if below != nil {
		rules = append(rules, RuleBelow(*below))
	}
	if spikeRatioMilli > 0 {
		rules = append(rules, RuleBurnSpike(spikeRatioMilli))
	}
	if dropRatioMilli > 0 {
		for _, k := range cliffKeys {
			rules = append(rules, RuleCliff(k, dropRatioMilli))
		}
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

// CliffTransition 单 key 相邻两次成功快照的余额转移(骤降判据)。
type CliffTransition struct {
	KeyID        string
	PrevCredits  int64  // 前值快照余额(credit);≤0 = 前值非正,判据不可得
	CurrCredits  int64  // 当前快照余额(credit)
	SameCurrency bool   // 前后币种一致;false = 不判定
	Currency     string // 当前币种(载荷上下文)
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
	// SpikeRatioMilli 燃速突变判据:当前窗口日均÷上一等长窗口日均(毫单位);
	// nil = 判据不可得(任一窗口样本不足/零基线/零消耗/禁用),状态保持。
	SpikeRatioMilli *int64
	PrevBurnPerDay  int64 // 载荷上下文:上一窗口日均(credit/天)
	CurBurnPerDay   int64 // 载荷上下文:当前窗口日均(credit/天)
	// CliffTransitions key_id → 最近相邻转移;键缺失 = 该 key 无转移可判,状态保持。
	CliffTransitions map[string]CliffTransition
}

// Evaluate 告警状态机(纯函数):
//
//	armed → firing:跌破触发线(below:余额 < 阈值;tier:剩余 ≤ 档位;burn_spike:比值 ≥
//	触发线;cliff:跌幅 ≥ 触发线),发射一次事件
//	firing → armed:回升超 触发线 × 迟滞系数(below/tier)或回落到 触发线 ÷ 迟滞系数
//	(burn_spike)或出现一次平稳观测(cliff),静默解除(不发事件)
//	判据不可得(余额不可知/无估算/基线不足/无转移)时状态保持原样——既不误触发也不误解除
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
		switch r.Kind {
		case RuleKindBurnSpike:
			if in.SpikeRatioMilli == nil {
				continue // 判据不可得:状态保持
			}
			ratio := *in.SpikeRatioMilli
			if !st.Firing {
				if ratio >= r.SpikeRatioMilli {
					f := Firing{
						RuleKey: r.Key, Severity: "warning", At: in.Now,
						Summary: fmt.Sprintf("渠道 %s 日均消耗突增至约 %s 倍(上一窗口日均 %s credit/天 → 当前 %s)",
							in.Provider, ratioText(ratio), FormatCredits(in.PrevBurnPerDay), FormatCredits(in.CurBurnPerDay)),
						Context: map[string]any{
							"provider": in.Provider, "rule": r.Key,
							"prev_burn_per_day_credits": in.PrevBurnPerDay, "cur_burn_per_day_credits": in.CurBurnPerDay,
							"ratio_milli": ratio,
						},
					}
					firings = append(firings, f)
					st.Firing, st.Severity, st.LastFiredAt, st.ContextJSON = true, f.Severity, &f.At, marshalContext(f)
				}
			} else if ratio < scaledLineDiv(r.SpikeRatioMilli, in.RearmMilli) {
				st.Firing, st.ContextJSON = false, "" // 回落超迟滞线,重新武装
			}
		case RuleKindCliff:
			tr, ok := in.CliffTransitions[r.CliffKeyID]
			if !ok || !tr.SameCurrency || tr.PrevCredits <= 0 {
				continue // 无转移/币种不一致/前值非正:判据不可得,状态保持
			}
			dropMilli := (tr.PrevCredits - tr.CurrCredits) * 1000 / tr.PrevCredits
			stable := dropMilli < r.CliffRatioMilli // 平稳观测:跌幅低于阈值(含余额上升)
			if !st.Firing {
				if !stable {
					f := Firing{
						RuleKey: r.Key, Severity: "warning", At: in.Now,
						Summary: fmt.Sprintf("渠道 %s 的 key %s 余额骤降约 %s%%(%s → %s %s)",
							in.Provider, tr.KeyID, percentText(dropMilli),
							FormatCredits(tr.PrevCredits), FormatCredits(tr.CurrCredits), tr.Currency),
						Context: map[string]any{
							"provider": in.Provider, "rule": r.Key, "key_id": tr.KeyID,
							"prev_balance": FormatCredits(tr.PrevCredits), "curr_balance": FormatCredits(tr.CurrCredits),
							"currency": tr.Currency, "drop_ratio_milli": dropMilli,
						},
					}
					firings = append(firings, f)
					st.Firing, st.Severity, st.LastFiredAt, st.ContextJSON = true, f.Severity, &f.At, marshalContext(f)
				}
			} else if stable {
				st.Firing, st.ContextJSON = false, "" // 平稳观测:重新武装
			}
		default: // below 与 tier 判定不变
			switch {
			case r.IsTier():
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

// scaledLineDiv 「高为坏」类规则(燃速突变)的迟滞解除线 = base × 1000 ÷ milli:
// 触发线 5000、迟滞 1.2 倍时解除线 ≈4166(比值回落到触发线 ÷ 1.2 以下解除)。
// milli 由配置校验兜底 ≥1000,除法不除零。
func scaledLineDiv(base, milli int64) int64 {
	return base * 1000 / milli
}

// ratioText 毫单位比值的人类文案(6667 → "6.667");展示层专用,不参与判定运算。
func ratioText(milli int64) string {
	return fmt.Sprintf("%d.%03d", milli/1000, milli%1000)
}

// percentText 毫单位比例的百分比文案(600 → "60.0")。
func percentText(milli int64) string {
	return fmt.Sprintf("%.1f", float64(milli)/10)
}
