// Package metering 承担计量计费:配价、成本换算与 usage 落库。
// 计价契约见 docs/design/metering.md「计价」:内部记账单位为 **credit**,
// 1 credit = 账单币种的十亿分之一(1e-9;如 CNY 租户 1 credit = 1e-9 元)。
// 选型依据:①廉价档(如 $0.003/百万Token)在 1e-9 下逐 token 恰为整数 credit(3),
// 心智换算干净;μ 基座则连 0.02μ/token 都无法整表达,逐段 ceil 产生整数倍失真。
// ②int64 安全裕度按入站 32MB 体上限(≈1e7 token)核算:1e7 × $200/M(2e11) = 2e18
// 仍留 4.6× 裕度;再细一位(1e-10)该乘积即溢出,需 128 位中间量——过度设计。
// 配置侧人类可读价目在加载期一次性换算为整数 credit/百万Token,此后全链路禁 float。
package metering

import (
	"errors"
	"math"
	"strconv"
	"strings"
)

// ErrInvalidPrice 非法价目字串:非纯十进制、带符号、超 9 位小数或解析溢出。
// 哨兵错误,加载期用 errors.Is 统一归类为配置错误。
var ErrInvalidPrice = errors.New("metering: 非法价目字串")

// creditPerUnit 记账单位换算基数:1 元(本币) = 1e9 credit。
const creditPerUnit = 1_000_000_000

// ParsePrice 把人类可读十进制价目("2.00" 表示 2.00 元/百万Token)换算为整数 credit/百万Token。
// 逐位十进制解析、不经 float;小数最多 9 位(credit 精度上限),超出即报错。
// 只接受 [0-9]+(.[0-9]{1,9})? 形态:不接受符号、空白、科学计数法与缺位小数点。
func ParsePrice(human string) (int64, error) {
	intPart, fracPart, ok := strings.Cut(human, ".")
	if !ok {
		fracPart = ""
	}
	if intPart == "" || fracPart == "" && ok {
		// ".5" 与 "2." 均拒绝:配置价目必须写全,避免歧义
		return 0, ErrInvalidPrice
	}
	// 先做纯数字校验再 ParseInt:strconv 接受 +/- 前缀,负价/显式符号必须在此拦截
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
	// 乘 1e9 换算前做溢出防护:整数部分过大时乘积翻转为负,负价会静默毒化 SUM(cost_credits) 对账。
	// 两级守卫:①整数部分 > MaxInt64/1e9 一律拒绝;②恰在边界(intVal == 上界)时,
	// 头顶余量 MaxInt64%1e9(=854775807)小于最大小数补位(999999999),
	// 此时小数部分 > 余量同样回绕为负(域A 审查实证 "9223372036.9" → 负值)——窗口一并拒绝。
	maxInt := int64(math.MaxInt64 / creditPerUnit)
	if intVal > maxInt ||
		(intVal == maxInt && fracPart != "" && strings.Trim(fracPart, "0") != "") {
		return 0, ErrInvalidPrice
	}
	// 小数部分右侧补零对齐到 9 位;全零补齐不改变数值。
	// 边界小数(整数恰为上界)已在上方守卫拒绝非零小数;此处 fracVal 仅在
	// intVal < 上界 或 (== 上界且小数全零→fracVal=0) 时到达,无溢出可能。
	fracVal := int64(0)
	if fracPart != "" {
		fracVal, err = strconv.ParseInt(fracPart+strings.Repeat("0", 9-len(fracPart)), 10, 64)
		if err != nil {
			return 0, ErrInvalidPrice
		}
	}
	return intVal*creditPerUnit + fracVal, nil
}

// isDigits 判定字串是否全为 ASCII 数字(空串视为合法,由调用方先做缺位检查)。
func isDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// tokensPerM 价格的「百万 Token」归一化分母(与货币刻度无关的两个独立常数,
// 不可混用:creditPerUnit 是币值刻度 1e9,tokensPerM 恒为 1e6)。
const tokensPerM = 1_000_000

// Cost 按计费契约计算单请求成本(credit):逐段 ceil(tokens×价格/1e6) 后求和,
// 逐段独立向上取整保证对供应商友好(单请求误差 ≤2 credit = 2e-9 本币)。
// 价格入参即 ParsePrice 的输出(credit/百万Token);token 数与价格均为非负整数域。
func Cost(inTokens, outTokens, inCreditsPerM, outCreditsPerM int64) int64 {
	return CostWithCached(inTokens, 0, outTokens, inCreditsPerM, 0, outCreditsPerM)
}

// CostWithCached 三段计价(metering.md「缓存价」契约):
// 未命中输入 × input 价 + 缓存命中输入 × cached 价 + 输出 × output 价,逐段独立向上取整。
// cachedCreditsPerM=0 视为与输入同价(缺省=input);cached 超过 prompt 时钳制(防御脏数据)。
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
	return ceilDiv((promptTokens-cachedTokens)*inCreditsPerM, tokensPerM) +
		ceilDiv(cachedTokens*cachedCreditsPerM, tokensPerM) +
		ceilDiv(outTokens*outCreditsPerM, tokensPerM)
}

// ceilDiv 非负整数除法向上取整;0 除以任何正数得 0。
func ceilDiv(n, d int64) int64 {
	q, r := n/d, n%d
	if r > 0 {
		q++
	}
	return q
}
