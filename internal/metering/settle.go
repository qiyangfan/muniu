// 本位币结算(metering.md「本位币与 credit」):
// credit 锚定网关级唯一**本位币**(settlement:CNY 或 USD),不锚定各上游的
// 「账单币种」——否则同一配额池在 CNY 模型与 USD 模型下消耗速率随汇率漂移,语义自相矛盾。
// 异币种价目(厂商美元原报价)在加载期按汇率一次性换算为本位币 credit;
// 汇率方向恒定「1 USD = fx/1000 CNY」(毫单位整数,零浮点);热路径与配额域单一币种。
package metering

import "math"

// SettlementCNY / SettlementUSD 本位币合法取值(与价目币种字典一致;全球模型报价
// 事实上仅 CNY/USD 两档本位,其余币种不做)。
const (
	SettlementCNY = "CNY"
	SettlementUSD = "USD"
)

// ValidSettlement 本位币取值校验。
func ValidSettlement(s string) bool {
	return s == SettlementCNY || s == SettlementUSD
}

// SettlePrice 报价价目 → 本位币价目:币种相同原样返回;异币种按汇率换算,
// 逐段独立 ceil(对供应商友好口径,宁多勿少)。
// fxCnyPerUsdMilli 恒为「1 USD = fx/1000 CNY」(如 7200 = 7.200),方向固定不取倒数。
// fxMilli ≤ 0 且币种不同 → 第二返回值 false(调用方 fail-fast,禁止静默混币种)。
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
	// 方向判定:USD 报价→CNY 本位 = 乘汇率;CNY 报价→USD 本位 = 除汇率(ceil)
	var num, denom int64 = 1000, 1000
	switch {
	case quote.Currency == SettlementUSD && settlement == SettlementCNY:
		num = fxCnyPerUsdMilli
	case quote.Currency == SettlementCNY && settlement == SettlementUSD:
		denom = fxCnyPerUsdMilli
	default:
		return Pricing{}, false // 同币种已在上方短路;剩余组合为非法币种
	}
	// 乘法溢出守卫:极大价目 × 极大汇率会回绕为负(域A 审查实证 9.2e18×7200);
	// 任一段乘积越界即整体 fail-fast,绝不放行负价进价目表
	if quote.InputCreditsPerM > math.MaxInt64/num ||
		quote.CachedInputCreditsPerM > math.MaxInt64/num ||
		quote.OutputCreditsPerM > math.MaxInt64/num {
		return Pricing{}, false
	}
	out := Pricing{Currency: settlement}
	out.InputCreditsPerM = ceilDiv(quote.InputCreditsPerM*num, denom)
	out.CachedInputCreditsPerM = ceilDiv(quote.CachedInputCreditsPerM*num, denom)
	out.OutputCreditsPerM = ceilDiv(quote.OutputCreditsPerM*num, denom)
	return out, true
}
