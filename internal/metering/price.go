// Package metering 承担计量计费：配价、成本换算与 usage 落库。
// 内部记账单位为 credit，1 credit = 账单币种的十亿分之一（1e-9），如 CNY 租户 1 credit = 1e-9 元。
// 选型依据：低价档如 $0.003/百万 Token 在 1e-9 下逐 token 恰为整数 credit（3），μ 基座无法整表达。
// 除以百万前的乘积可能超出 int64，乘除过程必须使用宽整数中间量。
// 配置侧人类可读价目在加载期一次性换算为整数 credit/百万 Token，此后全程禁 float。
package metering

import (
	"errors"
	"math"
	"math/bits"
	"strconv"
	"strings"
)

// ErrInvalidPrice 非法价目字串：非纯十进制、带符号、超 9 位小数或解析溢出。
// 哨兵错误，加载期用 errors.Is 统一归类为配置错误。
var ErrInvalidPrice = errors.New("metering: 非法价目字串")

// creditPerUnit 记账单位换算基数：1 元本币 = 1e9 credit。
const creditPerUnit = 1_000_000_000

// ParsePrice 把人类可读十进制价目换算为整数 credit/百万Token，如 "2.00" 表示 2.00 元/百万Token。
// 逐位十进制解析、不经 float；小数最多 9 位即 credit 精度上限，超出即报错。
// 只接受 [0-9]+(.[0-9]{1,9})? 形态：不接受符号、空白、科学计数法与缺位小数点。
func ParsePrice(human string) (int64, error) {
	intPart, fracPart, ok := strings.Cut(human, ".")
	if !ok {
		fracPart = ""
	}
	if intPart == "" || fracPart == "" && ok {
		// ".5" 与 "2." 均拒绝：配置价目必须写全，避免歧义。
		return 0, ErrInvalidPrice
	}
	// 先做纯数字校验再 ParseInt：strconv 接受 +/- 前缀，负价/显式符号必须在此拦截。
	if !isDigits(intPart) || !isDigits(fracPart) {
		return 0, ErrInvalidPrice
	}
	if len(fracPart) > 9 {
		return 0, ErrInvalidPrice
	}
	intVal, err := strconv.ParseInt(intPart, 10, 64)
	if err != nil {
		return 0, ErrInvalidPrice
	}
	// 乘 1e9 换算前做溢出防护：整数部分过大时乘积翻转为负，负价会静默毒化 SUM(cost_credits) 对账。
	// 边界整数部分仍可能因小数部分溢出，因此此处一并拒绝非零小数。
	maxInt := int64(math.MaxInt64 / creditPerUnit)
	if intVal > maxInt ||
		(intVal == maxInt && fracPart != "" && strings.Trim(fracPart, "0") != "") {
		return 0, ErrInvalidPrice
	}
	// 小数部分右侧补零对齐到 9 位；全零补齐不改变数值。
	// 边界小数即整数恰为上界的情况，已在上方守卫拒绝非零小数；此处 fracVal 仅在
	// intVal < 上界、或整数恰为上界且小数全零(fracVal=0)时到达，无溢出可能。
	fracVal := int64(0)
	if fracPart != "" {
		fracVal, err = strconv.ParseInt(fracPart+strings.Repeat("0", 9-len(fracPart)), 10, 64)
		if err != nil {
			return 0, ErrInvalidPrice
		}
	}
	return intVal*creditPerUnit + fracVal, nil
}

// isDigits 判定字串是否全为 ASCII 数字；空串视为合法，由调用方先做缺位检查。
func isDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// tokensPerM 价格的「百万 Token」归一化分母，与 creditPerUnit 是货币刻度无关的两个独立常数，
// 不可混用：creditPerUnit 是币值刻度 1e9，tokensPerM 恒为 1e6。
const tokensPerM = 1_000_000

// Cost 计算单请求成本，单位 credit：逐段 ceil(tokens×价格/1e6) 后求和，
// 逐段独立向上取整保证对供应商友好，单请求误差 ≤2 credit = 2e-9 本币。
// 价格入参即 ParsePrice 的输出 credit/百万Token；token 数与价格均为非负整数域。
// 金额不可表示或入参越出非负域时返回 -1，调用方必须作为计价失败处理，不能结算。
func Cost(inTokens, outTokens, inCreditsPerM, outCreditsPerM int64) int64 {
	return CostWithCached(inTokens, 0, outTokens, inCreditsPerM, 0, outCreditsPerM)
}

// CostWithCached 三段计价：未命中输入 × input 价 + 缓存命中输入 × cached 价 + 输出 × output 价，
// 逐段独立向上取整。cachedCreditsPerM=0 视为与输入同价；cached 超过 prompt 时钳制，防御脏数据。
// -1 仅为失败信号，NewRow 将其转换为未结算观测，绝不写入 cost_credits。
func CostWithCached(promptTokens, cachedTokens, outTokens, inCreditsPerM, cachedCreditsPerM, outCreditsPerM int64) int64 {
	if cachedCreditsPerM <= 0 {
		cachedCreditsPerM = inCreditsPerM
	}
	if cachedTokens > promptTokens {
		cachedTokens = promptTokens
	}
	if cachedTokens < 0 {
		cachedTokens = 0
	}
	if promptTokens < 0 || outTokens < 0 {
		return -1
	}
	var total int64
	for _, part := range [3]struct{ tokens, price int64 }{
		{promptTokens - cachedTokens, inCreditsPerM},
		{cachedTokens, cachedCreditsPerM},
		{outTokens, outCreditsPerM},
	} {
		amount, valid := CostForTokens(uint64(part.tokens), part.price)
		if !valid || amount > math.MaxInt64-total {
			return -1
		}
		total += amount
	}
	return total
}

// CostWithVision 在输入中拆分文本、缓存和视觉 token，各段独立向上取整。
// 视觉单价为 0 时回落 input；合法纯文本用量复用旧公式，避免改变既有费用。
func CostWithVision(promptTokens, cachedTokens, visionTokens, outTokens,
	inCreditsPerM, cachedCreditsPerM, visionCreditsPerM, outCreditsPerM int64) int64 {
	if promptTokens < 0 || cachedTokens < 0 || visionTokens < 0 || outTokens < 0 ||
		cachedTokens > promptTokens || visionTokens > promptTokens-cachedTokens || visionCreditsPerM < 0 {
		return -1
	}
	if visionTokens == 0 {
		return CostWithCached(promptTokens, cachedTokens, outTokens, inCreditsPerM, cachedCreditsPerM, outCreditsPerM)
	}
	if cachedCreditsPerM <= 0 {
		cachedCreditsPerM = inCreditsPerM
	}
	if visionCreditsPerM == 0 {
		visionCreditsPerM = inCreditsPerM
	}
	var total int64
	for _, part := range [4]struct{ tokens, price int64 }{
		{promptTokens - cachedTokens - visionTokens, inCreditsPerM},
		{cachedTokens, cachedCreditsPerM},
		{visionTokens, visionCreditsPerM},
		{outTokens, outCreditsPerM},
	} {
		amount, valid := CostForTokens(uint64(part.tokens), part.price)
		if !valid || amount > math.MaxInt64-total {
			return -1
		}
		total += amount
	}
	return total
}

// ceilDiv 非负整数除法向上取整；0 除以任何正数得 0。
func ceilDiv(n, d int64) int64 {
	q, r := n/d, n%d
	if r > 0 {
		q++
	}
	return q
}

// CostForTokens 逐段向上取整，以宽中间量保留可表示的最终 credit；false 表示不能结算。
// token 累计允许 uint64，以容纳预算检查中“usage 真值 + 后续估算”的临时总量。
func CostForTokens(tokens uint64, creditsPerM int64) (int64, bool) {
	if creditsPerM < 0 {
		return 0, false
	}
	hi, lo := bits.Mul64(tokens, uint64(creditsPerM))
	if hi >= tokensPerM {
		return 0, false
	}
	quotient, remainder := bits.Div64(hi, lo, tokensPerM)
	if quotient > math.MaxInt64 {
		return 0, false
	}
	if remainder > 0 {
		quotient++
	}
	if quotient > math.MaxInt64 {
		return 0, false
	}
	return int64(quotient), true
}
