// 计价换算的表驱动测试:字符串解析(禁 float)与逐段向上取整求和。
// 契约依据 docs/design/metering.md「计价」;负面用例必须先于实现存在(测试先行域)。
package metering

import (
	"errors"
	"testing"
)

func TestParsePrice(t *testing.T) {
	cases := []struct {
		name  string
		human string
		want  int64
	}{
		{"整数元", "2", 2_000_000},
		{"两位小数", "2.00", 2_000_000},
		{"零", "0", 0},
		{"全零小数", "0.000000", 0},
		{"典型输入价", "0.15", 150_000},
		{"最小有效粒度", "0.000001", 1},
		{"整数部分带进位", "123456.123456", 123_456_123_456},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ParsePrice(c.human)
			if err != nil {
				t.Fatalf("ParsePrice(%q) 意外报错: %v", c.human, err)
			}
			if got != c.want {
				t.Fatalf("ParsePrice(%q) = %d, 期望 %d", c.human, got, c.want)
			}
		})
	}
}

func TestParsePrice_非法字串(t *testing.T) {
	// 全部必须返回 ErrInvalidPrice 哨兵,便于加载期 errors.Is 统一归类
	cases := []string{
		"",                           // 空串
		"   ",                        // 纯空白
		"abc",                        // 非数字
		"1.2.3",                      // 多个小数点
		"-1.00",                      // 负价无意义
		"+1.00",                      // 显式符号不接受
		"1e6",                        // 科学计数法:必须逐位十进制
		"0x10",                       // 十六进制
		" 2.00",                      // 前导空白
		"2.00 ",                      // 尾随空白
		".5",                         // 缺整数部分
		"2.",                         // 缺小数部分
		"0.0000001",                  // 7 位小数,超出 μ 精度
		"99999999999999999999999999", // 解析溢出 int64
	}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			got, err := ParsePrice(in)
			if !errors.Is(err, ErrInvalidPrice) {
				t.Fatalf("ParsePrice(%q) 期望 ErrInvalidPrice, 实际 got=%d err=%v", in, got, err)
			}
		})
	}
}

func TestParsePrice_换算溢出(t *testing.T) {
	// ParseInt 能容纳但 ×1e6 换算会翻转成负数的整数部分:负价会静默毒化 SUM(cost_micro)
	// 上界 = MaxInt64/1e6 = 9223372036853 元(≈9.2 万亿)
	for _, in := range []string{"9223372036855", "10000000000000000"} {
		got, err := ParsePrice(in)
		if !errors.Is(err, ErrInvalidPrice) {
			t.Fatalf("ParsePrice(%q) 换算溢出应报错, 实际 got=%d err=%v", in, got, err)
		}
	}
	if got, err := ParsePrice("9223372036853"); err != nil || got != 9_223_372_036_853_000_000 {
		t.Fatalf("换算上界内应合法: got=%d err=%v", got, err)
	}
}

func TestCost(t *testing.T) {
	cases := []struct {
		name                string
		inTokens, outTokens int64
		inMicro, outMicro   int64
		want                int64
	}{
		{"零 token 零成本", 0, 0, 2_000_000, 8_000_000, 0},
		{"tokenctl 样例 1200/800", 1200, 800, 2_000_000, 8_000_000, 8_800},
		{"恰 1e6 输入 token", 1_000_000, 0, 2_000_000, 8_000_000, 2_000_000},
		{"恰 1e6 输出 token", 0, 1_000_000, 2_000_000, 8_000_000, 8_000_000},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Cost(c.inTokens, c.outTokens, c.inMicro, c.outMicro)
			if got != c.want {
				t.Fatalf("Cost(%d,%d,%d,%d) = %d, 期望 %d",
					c.inTokens, c.outTokens, c.inMicro, c.outMicro, got, c.want)
			}
		})
	}
}

func TestCost_向上取整(t *testing.T) {
	// 契约:cost_micro = ceil(in×p_in/1e6) + ceil(out×p_out/1e6),逐段独立取整
	cases := []struct {
		name              string
		tokens, microPerM int64
		want              int64
	}{
		{"1 token 最小价也要 1μ", 1, 1, 1},
		{"不足 1μ 向上取整", 999_999, 1, 1},
		{"超出整 1μ 进位", 1_000_001, 1, 2},
		{"恰除尽不进位", 500_000, 2, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Cost(c.tokens, 0, c.microPerM, 0)
			if got != c.want {
				t.Fatalf("Cost(in=%d, p=%d) = %d, 期望 %d", c.tokens, c.microPerM, got, c.want)
			}
		})
	}
}

func TestCostWithCached_基本语义(t *testing.T) {
	// cached=0:与两段 Cost 完全等价
	if got := CostWithCached(1200, 0, 800, 2_000_000, 0, 8_000_000); got != Cost(1200, 800, 2_000_000, 8_000_000) {
		t.Fatalf("cached=0 应等价两段: %d vs %d", got, Cost(1200, 800, 2_000_000, 8_000_000))
	}
	// 三段独立取整:miss=400(×2 = 800μ) + cached=600(×1 = 600μ) + out=500(×8 = 4000μ)
	if got := CostWithCached(1000, 600, 500, 2_000_000, 1_000_000, 8_000_000); got != 800+600+4000 {
		t.Fatalf("三段求和不符: %d", got)
	}
	// cached 超过 prompt:钳制到 prompt(防御脏数据)
	if got := CostWithCached(100, 500, 0, 1_000_000, 0, 1_000_000); got != 100 {
		t.Fatalf("cached 钳制后应只剩输入段: %d", got)
	}
}
