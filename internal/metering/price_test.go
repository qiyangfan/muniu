// 计价换算的表驱动测试：字符串解析禁 float，逐段向上取整求和。
package metering

import (
	"errors"
	"math"
	"math/big"
	"math/rand"
	"testing"
)

func TestParsePrice(t *testing.T) {
	cases := []struct {
		name  string
		human string
		want  int64
	}{
		{"整数元", "2", 2_000_000_000},
		{"两位小数", "2.00", 2_000_000_000},
		{"零", "0", 0},
		{"全零小数", "0.00000000", 0},
		{"典型输入价", "0.15", 150_000_000},
		{"最小有效粒度", "0.000001", 1_000},
		{"美元廉价档逐token精确", "0.003", 3_000_000},
		{"九位小数下界", "0.000000001", 1},
		{"整数部分带进位", "123456.123456", 123_456_123_456_000},
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
	// 全部必须返回 ErrInvalidPrice 哨兵，便于加载期 errors.Is 统一归类。
	cases := []string{
		"",                           // 空串
		"   ",                        // 纯空白
		"abc",                        // 非数字
		"1.2.3",                      // 多个小数点
		"-1.00",                      // 负价无意义
		"+1.00",                      // 显式符号不接受
		"1e6",                        // 科学计数法：必须逐位十进制。
		"0x10",                       // 十六进制
		" 2.00",                      // 前导空白
		"2.00 ",                      // 尾随空白
		".5",                         // 缺整数部分
		"2.",                         // 缺小数部分
		"0.0000000001",               // 10 位小数，超出 credit 精度。
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
	// ParseInt 能容纳但 ×1e9 换算会翻转成负数的整数部分：负价会静默毒化 SUM(cost_credits)。
	// 上界 = MaxInt64/1e9 = 9223372036 元，约 92 亿。
	for _, in := range []string{"9223372037", "100000000000000"} {
		got, err := ParsePrice(in)
		if !errors.Is(err, ErrInvalidPrice) {
			t.Fatalf("ParsePrice(%q) 换算溢出应报错, 实际 got=%d err=%v", in, got, err)
		}
	}
	if got, err := ParsePrice("9223372036"); err != nil || got != 9_223_372_036_000_000_000 {
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
		{"零 token 零成本", 0, 0, 2_000_000_000, 8_000_000_000, 0},
		{"tokenctl 样例 1200/800", 1200, 800, 2_000_000_000, 8_000_000_000, 8_800_000},
		{"恰 1e6 输入 token", 1_000_000, 0, 2_000_000_000, 8_000_000_000, 2_000_000_000},
		{"恰 1e6 输出 token", 0, 1_000_000, 2_000_000_000, 8_000_000_000, 8_000_000_000},
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
	// cost_credits = ceil(in×p_in/1e6) + ceil(out×p_out/1e6)，逐段独立取整。
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
	// cached=0 时与两段 Cost 完全等价。
	if got := CostWithCached(1200, 0, 800, 2_000_000_000, 0, 8_000_000_000); got != Cost(1200, 800, 2_000_000_000, 8_000_000_000) {
		t.Fatalf("cached=0 应等价两段: %d vs %d", got, Cost(1200, 800, 2_000_000_000, 8_000_000_000))
	}
	// 三段独立取整：miss=400 未命中 ×2000/token + cached=600 ×1000/token + out=500 ×8000/token。
	if got := CostWithCached(1000, 600, 500, 2_000_000_000, 1_000_000_000, 8_000_000_000); got != 800_000+600_000+4_000_000 {
		t.Fatalf("三段求和不符: %d", got)
	}
	// cached 超过 prompt 时钳制到 prompt，防御脏数据。
	// 钳制后 = prompt(100)×price(1e3 credit/M) = 0.1 credit → ceil 1，逐段取整口径。
	if got := CostWithCached(100, 500, 0, 1_000, 0, 1_000); got != 1 {
		t.Fatalf("cached 钳制后应只剩输入段(ceil 后 1 credit): %d", got)
	}
}

func TestCostWithCached_宽乘积精确计价(t *testing.T) {
	for _, c := range []struct {
		name                                                               string
		prompt, cached, output, inputPrice, cachedPrice, outputPrice, want int64
	}{
		{"已报告回绕", 100_000_000, 0, 0, 1_000_000_000_000, 0, 0, 100_000_000_000_000},
		{"三段宽乘积", 100_000_000, 50_000_000, 50_000_000, 1_000_000_000_000, 500_000_000_000, 2_000_000_000_000, 175_000_000_000_000},
		{"缓存价回落", 100_000_000, 50_000_000, 0, 1_000_000_000_000, 0, 0, 100_000_000_000_000},
		{"缓存钳制", 100_000_000, math.MaxInt64, 0, 1_000_000_000_000, 500_000_000_000, 0, 50_000_000_000_000},
		{"宽乘积向上取整", math.MaxInt64, 0, 0, 2, 0, 0, 18_446_744_073_710},
		{"最终金额恰为上界", 1_000_000, 0, 0, math.MaxInt64, 0, 0, math.MaxInt64},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := CostWithCached(c.prompt, c.cached, c.output, c.inputPrice, c.cachedPrice, c.outputPrice)
			if got != c.want {
				t.Fatalf("成本 got=%d want=%d", got, c.want)
			}
		})
	}
}

func TestCostWithCached_无法表示金额显式失败(t *testing.T) {
	for _, c := range [][6]int64{
		{math.MaxInt64, 0, 0, math.MaxInt64, 0, 0},
		{1_000_000, 0, 1, math.MaxInt64, 0, 1},
		{1_000_000, 0, 1_000_000, math.MaxInt64/2 + 1, 0, math.MaxInt64/2 + 1},
	} {
		if got := CostWithCached(c[0], c[1], c[2], c[3], c[4], c[5]); got >= 0 {
			t.Fatalf("不能把溢出费用伪报为有效值: %d", got)
		}
	}
}

func TestCostWithCached_三段大整数对拍(t *testing.T) {
	r := rand.New(rand.NewSource(13))
	ceil := func(tokens, price int64) *big.Int {
		n := new(big.Int).Mul(big.NewInt(tokens), big.NewInt(price))
		n.Add(n, big.NewInt(tokensPerM-1))
		return n.Quo(n, big.NewInt(tokensPerM))
	}
	for i := 0; i < 500; i++ {
		prompt, cached, output := r.Int63(), r.Int63(), r.Int63()
		in, cache, out := r.Int63(), r.Int63(), r.Int63()
		if i%2 == 0 {
			prompt %= 100_000_000
			cached %= 100_000_000
			output %= 100_000_000
			in %= 1_000_000_000_000_000
			cache %= 1_000_000_000_000_000
			out %= 1_000_000_000_000_000
		}
		if i%7 == 0 {
			cache = 0
		}
		got := CostWithCached(prompt, cached, output, in, cache, out)
		if cache == 0 {
			cache = in
		}
		if cached > prompt {
			cached = prompt
		}
		expected := new(big.Int).Add(ceil(prompt-cached, in), ceil(cached, cache))
		expected.Add(expected, ceil(output, out))
		want := int64(-1)
		if expected.IsInt64() {
			want = expected.Int64()
		}
		if got != want {
			t.Fatalf("三段对拍第 %d 组: got=%d want=%s", i, got, expected.String())
		}
	}
}
