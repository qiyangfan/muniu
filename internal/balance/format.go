package balance

import (
	"fmt"
	"strings"
)

// FormatCredits credit 整数转人类可读十进制串(纯字符串操作,零 float):
// 12_340_000_000 → "12.34";负值带符号;-500_000_000 → "-0.50"。
// 供告警摘要与人工核对,存储与运算恒为整数 credit。
func FormatCredits(c int64) string {
	neg := c < 0
	if neg {
		c = -c
	}
	intPart := c / 1_000_000_000
	frac := c % 1_000_000_000
	s := fmt.Sprintf("%d.%09d", intPart, frac)
	s = strings.TrimRight(s, "0")
	s = strings.TrimSuffix(s, ".")
	if s == "" {
		s = "0"
	}
	if neg {
		return "-" + s
	}
	return s
}

// HumanRemaining 剩余小时的人类表述:"3 天 4 小时"/"5 小时"。估算口径为网关视角,
// 调用方文案应带「约」。
func HumanRemaining(hours int64) string {
	switch {
	case hours < 0:
		return "已透支"
	case hours < 24:
		return fmt.Sprintf("%d 小时", hours)
	default:
		days := hours / 24
		rem := hours % 24
		// 一周以上小时数无信息量,天数为主;一周内保留小时提高紧迫感读感
		if days >= 7 || rem == 0 {
			return fmt.Sprintf("%d 天", days)
		}
		return fmt.Sprintf("%d 天 %d 小时", days, rem)
	}
}
