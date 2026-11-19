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
	intPart, fracPart, ok := splitDecimal(human)
	if !ok || !isDigits(intPart) || !isDigits(fracPart) || len(fracPart) > 9 {
		// ".5" 与 "2." 缺位拒绝，配置价目必须写全；纯数字校验先于 ParseInt，
		// strconv 接受 +/- 前缀，负价/显式符号必须在此拦截。
		return 0, ErrInvalidPrice
	}
	intVal, err := strconv.ParseInt(intPart, 10, 64)
	if err != nil {
		return 0, ErrInvalidPrice
	}
	// 乘 1e9 换算前做溢出防护：整数部分过大时乘积翻转为负，负价会静默毒化 SUM(cost_credits) 对账。
	if intVal > maxPriceInt(int64(math.MaxInt64/creditPerUnit), fracPart) {
		return 0, ErrInvalidPrice
	}
	fracVal, err := parseScaledFraction(fracPart, 9)
	if err != nil {
		return 0, err
	}
	return intVal*creditPerUnit + fracVal, nil
}

// maxPriceInt 整数部分上界：恰为上界时小数必须全零。
// 边界小数即整数恰为上界的情况，此处拒绝非零小数后，fracVal 仅在上界内到达，乘法无溢出可能。
func maxPriceInt(maxInt int64, fracPart string) int64 {
	if fracPart != "" && strings.Trim(fracPart, "0") != "" {
		return maxInt - 1
	}
	return maxInt
}

// splitDecimal 按小数点切分人类可读十进制串，返回整数部分与小数部分。
// ok=false 表示形态缺位(intPart 为空，或带小数点但小数部分为空)，由调用方按纪律拒绝。
func splitDecimal(human string) (intPart, fracPart string, ok bool) {
	intPart, fracPart, hasDot := strings.Cut(human, ".")
	if !hasDot {
		fracPart = ""
	}
	if intPart == "" || fracPart == "" && hasDot {
		return intPart, fracPart, false
	}
	return intPart, fracPart, true
}

// parseScaledFraction 把纯数字小数部分右侧补零到 width 位后转整数；空串得 0。
func parseScaledFraction(fracPart string, width int) (int64, error) {
	if fracPart == "" {
		return 0, nil
	}
	v, err := strconv.ParseInt(fracPart+strings.Repeat("0", width-len(fracPart)), 10, 64)
	if err != nil {
		return 0, ErrInvalidPrice
	}
	return v, nil
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
func CostWithCached(
	promptTokens, cachedTokens, outTokens,
	inCreditsPerM, cachedCreditsPerM, outCreditsPerM int64,
) int64 {
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
	return sumParts([]costPart{
		{promptTokens - cachedTokens, inCreditsPerM},
		{cachedTokens, cachedCreditsPerM},
		{outTokens, outCreditsPerM},
	})
}

// CostWithVision 在输入中拆分文本、缓存和视觉 token，各段独立向上取整。
// 视觉单价为 0 时回落 input；合法纯文本用量复用旧公式，避免改变既有费用。
func CostWithVision(promptTokens, cachedTokens, visionTokens, outTokens,
	inCreditsPerM, cachedCreditsPerM, visionCreditsPerM, outCreditsPerM int64) int64 {
	if !visionUsageValid(promptTokens, cachedTokens, visionTokens, outTokens, visionCreditsPerM) {
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
	return sumParts([]costPart{
		{promptTokens - cachedTokens - visionTokens, inCreditsPerM},
		{cachedTokens, cachedCreditsPerM},
		{visionTokens, visionCreditsPerM},
		{outTokens, outCreditsPerM},
	})
}

// visionUsageValid 校验分段用量：各段非负、缓存与视觉不得超过扣除后的输入、视觉价非负。
func visionUsageValid(promptTokens, cachedTokens, visionTokens, outTokens, visionCreditsPerM int64) bool {
	return promptTokens >= 0 && cachedTokens >= 0 && visionTokens >= 0 && outTokens >= 0 &&
		cachedTokens <= promptTokens && visionTokens <= promptTokens-cachedTokens &&
		visionCreditsPerM >= 0
}

// costPart 单个计价段：token 数与对应 credit/百万Token 单价。
type costPart struct {
	tokens int64 // 非负；调用方已完成非负校验
	price  int64
}

// sumParts 逐段 ceil 求和；任一段不可表示或累计溢出即返回 -1。
func sumParts(parts []costPart) int64 {
	var total int64
	for _, part := range parts {
		amount, valid := costForNonNegativeTokens(part.tokens, part.price)
		if !valid || amount > math.MaxInt64-total {
			return -1
		}
		total += amount
	}
	return total
}

// costForNonNegativeTokens 计价段专用 CostForTokens：入参已由调用方校验非负，
// 负值在此一并拒绝，避免 int64→uint64 把负 token 翻转为巨值。
func costForNonNegativeTokens(tokens, creditsPerM int64) (int64, bool) {
	if tokens < 0 {
		return 0, false
	}
	return CostForTokens(uint64(tokens), creditsPerM)
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
