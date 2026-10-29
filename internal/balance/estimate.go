package balance

import "math"

// KeyBalance 单把 key 的余额观测输入(服务层从快照合成,估算纯函数不触库)。
type KeyBalance struct {
	KeyID    string
	Credits  *int64 // nil = 余额不可知(不限额键/无快照)
	Currency string
	// SpendCredits 快照时刻之后该渠道的已结算消耗(结算币种 credit);
	// 快照币种 ≠ 结算币种时服务层不衰减(传入 0 并保留原币种,估算侧判禁用)。
	SpendCredits int64
}

// Estimate 单渠道估算输出。
type Estimate struct {
	// MinEffective 渠道主币种(首个可知余额 key 的币种)下的最小有效余额;
	// 绝对阈值(below)判据与展示口径——below 声明在余额币种内,混合币种渠道
	// 只取主币种 key 参与 min,比较永不跨币种。nil = 无任何可知余额。
	MinEffective *int64
	// PrimaryCurrency 渠道主币种(首把可知余额 key 的币种)。
	PrimaryCurrency string
	// CurrencyMismatch 任一可知余额的币种 ≠ 结算币种;天数估算禁用,余额与阈值告警不受影响。
	CurrencyMismatch bool
	// BurnPerDay 窗口日均消耗(结算币种 credit);BurnCredits=0 时为 0。
	BurnPerDay int64
	// DaysWithUsage 窗口内有消耗(SUM>0)的自然日数,总览与最小样本判定同源。
	DaysWithUsage int
	// RemainingHours 剩余可用小时估算;nil = 无估算,原因见 Reason。
	RemainingHours *int64
	// Reason 无估算原因:空=有估算;no_usage=窗口零消耗;insufficient_data=消耗天数不足;
	// currency_mismatch=币种不一致;unknown_balance=全部余额不可知;
	// balance_overflow=余额超出可估算量级(任何档位都触不到,无告警意义)。
	Reason string
}

// 无估算原因常量(管理面按此映射降级文案)。
const (
	ReasonNoUsage          = "no_usage"
	ReasonInsufficientData = "insufficient_data"
	ReasonCurrencyMismatch = "currency_mismatch"
	ReasonUnknownBalance   = "unknown_balance"
	ReasonOverflow         = "balance_overflow"
)

// EstimateObservation 估算输入:服务层一次采集齐,估算零 IO。
type EstimateObservation struct {
	Keys          []KeyBalance
	Settlement    string // 结算币种 CNY|USD
	BurnCredits   int64  // 回看窗口已结算消耗合计
	DaysWithUsage int    // 窗口内 cost>0 的自然日数
	WindowDays    int
	MinSampleDays int
}

// EstimateChannel 燃速与剩余时间估算(纯函数,表驱动测试钉死):
// 剩余小时 = 最小有效余额 × 24 ÷ 日均燃速,全程整数;minEff 超过 int64 上界与 24 之比时
// 任何档位都触不到,直接无估算(钳制护栏)。
func EstimateChannel(obs EstimateObservation) Estimate {
	out := Estimate{}
	minPrimary, primary, mismatch := effectiveOf(obs.Keys, obs.Settlement)
	out.MinEffective, out.PrimaryCurrency, out.CurrencyMismatch = minPrimary, primary, mismatch

	if obs.BurnCredits <= 0 {
		out.Reason = ReasonNoUsage
		return out
	}
	out.DaysWithUsage = obs.DaysWithUsage
	out.BurnPerDay = obs.BurnCredits / int64(obs.WindowDays)
	if out.BurnPerDay <= 0 {
		// 窗口内有消耗但摊到日均不足 1 credit(<1e-9 币种单位/天):等效零耗,不产估算
		out.Reason = ReasonNoUsage
		return out
	}
	if obs.DaysWithUsage < obs.MinSampleDays {
		out.Reason = ReasonInsufficientData
		return out
	}
	if mismatch {
		out.Reason = ReasonCurrencyMismatch
		return out
	}
	if minPrimary == nil {
		out.Reason = ReasonUnknownBalance
		return out
	}
	const hoursScale = int64(24)
	if *minPrimary > math.MaxInt64/hoursScale {
		// 余额大到任何档位都触不到:不产估算,不告警;显式 reason,不留「空+nil」第三态
		out.Reason = ReasonOverflow
		return out
	}
	// 走到这里 mismatch 门已排除:全部可知余额均与结算币种一致,min 可安全作除数
	hours := *minPrimary * hoursScale / out.BurnPerDay
	out.RemainingHours = &hours
	return out
}

// effectiveOf 池内有效余额合成:逐 key 快照减快照后消耗,主币种(首个可知余额 key
// 的币种)内取最小。任一可知余额币种 ≠ 结算币种即判 mismatch(该 key 衰减禁用,
// 余额原值参与比较);below 与展示都吃主币种 min,比较永不跨币种混取。
func effectiveOf(keys []KeyBalance, settlement string) (*int64, string, bool) {
	var min *int64
	primary := ""
	mismatch := false
	for _, k := range keys {
		if k.Credits == nil {
			continue
		}
		if primary == "" {
			primary = k.Currency
		}
		eff := *k.Credits
		if k.Currency == settlement {
			eff -= k.SpendCredits
		} else {
			mismatch = true
		}
		if k.Currency == primary {
			if min == nil || eff < *min {
				v := eff
				min = &v
			}
		}
	}
	return min, primary, mismatch
}
