// Package metering 承担计量计费:配价、成本换算与 usage 落库。
// 计价契约见 docs/design/metering.md「计价」:内部记账单位为货币百万分之一(μ),
// 配置侧人类可读价目在加载期一次性换算为整数 μ/百万Token,此后全链路禁 float。
package metering

import (
	"errors"
	"math"
	"strconv"
	"strings"
)

// ErrInvalidPrice 非法价目字串:非纯十进制、带符号、超 6 位小数或解析溢出。
// 哨兵错误,加载期用 errors.Is 统一归类为配置错误。
var ErrInvalidPrice = errors.New("metering: 非法价目字串")

// microPerUnit 记账单位换算基数:1 元 = 1e6 μ。
const microPerUnit = 1_000_000

// ParsePrice 把人类可读十进制价目("2.00" 表示 2.00 元/百万Token)换算为整数 μ/百万Token。
// 逐位十进制解析、不经 float;小数最多 6 位(μ 精度上限),超出即报错。
// 只接受 [0-9]+(.[0-9]{1,6})? 形态:不接受符号、空白、科学计数法与缺位小数点。
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
	if len(fracPart) > 6 {
		return 0, ErrInvalidPrice
	}
	intVal, err := strconv.ParseInt(intPart, 10, 64)
	if err != nil {
		return 0, ErrInvalidPrice
	}
	// 乘 1e6 换算前做溢出防护:整数部分过大时乘积翻转为负,负价会静默毒化 SUM(cost_micro) 对账
	if intVal > (math.MaxInt64-microPerUnit+1)/microPerUnit {
		return 0, ErrInvalidPrice
	}
	// 小数部分右侧补零对齐到 6 位;全零补齐不改变数值
	fracVal := int64(0)
	if fracPart != "" {
		fracVal, err = strconv.ParseInt(fracPart+strings.Repeat("0", 6-len(fracPart)), 10, 64)
		if err != nil {
			return 0, ErrInvalidPrice
		}
	}
	return intVal*microPerUnit + fracVal, nil
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

// Cost 按计费契约计算单请求成本(μ):逐段 ceil(tokens×价格/1e6) 后求和,
// 逐段独立向上取整保证对供应商友好(单请求误差 ≤2μ)。
// 价格入参即 ParsePrice 的输出(μ/百万Token);token 数与价格均为非负整数域。
func Cost(inTokens, outTokens, inMicroPerM, outMicroPerM int64) int64 {
	return CostWithCached(inTokens, 0, outTokens, inMicroPerM, 0, outMicroPerM)
}

// CostWithCached 三段计价(metering.md「缓存价」契约):
// 未命中输入 × input 价 + 缓存命中输入 × cached 价 + 输出 × output 价,逐段独立向上取整。
// cachedMicroPerM=0 视为与输入同价(缺省=input);cached 超过 prompt 时钳制(防御脏数据)。
func CostWithCached(promptTokens, cachedTokens, outTokens, inMicroPerM, cachedMicroPerM, outMicroPerM int64) int64 {
	if cachedMicroPerM <= 0 {
		cachedMicroPerM = inMicroPerM
	}
	if cachedTokens > promptTokens {
		cachedTokens = promptTokens
	}
	if cachedTokens < 0 {
		cachedTokens = 0
	}
	return ceilDiv((promptTokens-cachedTokens)*inMicroPerM, microPerUnit) +
		ceilDiv(cachedTokens*cachedMicroPerM, microPerUnit) +
		ceilDiv(outTokens*outMicroPerM, microPerUnit)
}

// ceilDiv 非负整数除法向上取整;0 除以任何正数得 0。
func ceilDiv(n, d int64) int64 {
	q, r := n/d, n%d
	if r > 0 {
		q++
	}
	return q
}
