// 所有价格先换算为网关本位币，保证不同币种模型共用同一配额口径。
package metering

import "math"

// SettlementCNY 和 SettlementUSD 是支持的本位币。
const (
	SettlementCNY = "CNY"
	SettlementUSD = "USD"
)

// ValidSettlement 本位币取值校验。
func ValidSettlement(s string) bool {
	return s == SettlementCNY || s == SettlementUSD
}

// SettlePrice 报价价目 → 本位币价目：币种相同原样返回；异币种按汇率换算，
// 逐段独立 ceil，对供应商友好口径，宁多勿少。
// fxCnyPerUsdMilli 恒为 1 USD = fx/1000 CNY，如 7200 = 7.200，方向固定不取倒数。
// fxMilli ≤ 0 且币种不同 → 第二返回值 false，调用方 fail-fast，禁止静默混币种。
func SettlePrice(quote Pricing, settlement string, fxCnyPerUsdMilli int64) (Pricing, bool) {
	if quote.Currency == settlement || quote.Currency == "" {
		quote.Currency = settlement
		return quote, true
	}
	if fxCnyPerUsdMilli <= 0 {
		return Pricing{}, false
	}
	if !ValidSettlement(quote.Currency) || !ValidSettlement(settlement) {
		return Pricing{}, false
	}
	// 方向判定：USD 报价→CNY 本位 = 乘汇率；CNY 报价→USD 本位 = 除汇率(ceil)。
	var num, denom int64 = 1000, 1000
	switch {
	case quote.Currency == SettlementUSD && settlement == SettlementCNY:
		num = fxCnyPerUsdMilli
	case quote.Currency == SettlementCNY && settlement == SettlementUSD:
		denom = fxCnyPerUsdMilli
	default:
		return Pricing{}, false // 同币种已在上方短路；剩余组合为非法币种。
	}
	// 乘法溢出会生成负价格；任一段越界即拒绝整张价目。
	if quote.InputCreditsPerM > math.MaxInt64/num ||
		quote.CachedInputCreditsPerM > math.MaxInt64/num ||
		quote.VisionInputCreditsPerM > math.MaxInt64/num ||
		quote.OutputCreditsPerM > math.MaxInt64/num {
		return Pricing{}, false
	}
	out := Pricing{Currency: settlement}
	out.InputCreditsPerM = ceilDiv(quote.InputCreditsPerM*num, denom)
	out.CachedInputCreditsPerM = ceilDiv(quote.CachedInputCreditsPerM*num, denom)
	out.VisionInputCreditsPerM = ceilDiv(quote.VisionInputCreditsPerM*num, denom)
	out.OutputCreditsPerM = ceilDiv(quote.OutputCreditsPerM*num, denom)
	return out, true
}
