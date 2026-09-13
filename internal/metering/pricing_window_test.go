// 分时段倍率价目表契约测试(测试先行域):倍率解析(基点/零浮点)、窗口解析(跨午夜)、
// 时区固定偏移、先声明先匹配、保守向上取整、溢出防护。
// 契约:metering.md「计价」——配置人类可读,加载期一次性换算为整数,运行时零浮点查表。
package metering

import (
	"errors"
	"math"
	"testing"
	"time"
)

func TestParseMultiplier(t *testing.T) {
	cases := []struct {
		human string
		want  int64 // 基点
	}{
		{"0.5", 5000},
		{"1", 10000},
		{"2", 20000},
		{"0.25", 2500},
		{"0.0001", 1},    // 最小粒度:1 基点
		{"10", 100000},   // 上限 10 倍
		{"0.1234", 1234}, // 4 位小数即基点精度
	}
	for _, c := range cases {
		got, err := ParseMultiplier(c.human)
		if err != nil || got != c.want {
			t.Fatalf("ParseMultiplier(%q) = %d, %v; 期望 %d", c.human, got, err, c.want)
		}
	}
}

func TestParseMultiplier_非法(t *testing.T) {
	cases := []string{
		"", "abc", "-0.5", "+0.5", "0", "0.0000", // 非正
		"10.0001",                     // 超上限(>10 倍)
		"0.12345",                     // 超 4 位小数(超出基点精度)
		"1e-2", " 0.5 ", "0.5.", ".5", // 形态非法(与 ParsePrice 同纪律)
	}
	for _, in := range cases {
		if got, err := ParseMultiplier(in); !errors.Is(err, ErrInvalidPrice) {
			t.Fatalf("ParseMultiplier(%q) 期望 ErrInvalidPrice, 实际 got=%d err=%v", in, got, err)
		}
	}
}

func TestParseWindowMinutes(t *testing.T) {
	cases := []struct {
		in         string
		start, end int
		wantWrap   bool
	}{
		{"16:30-00:30", 16*60 + 30, 30, true}, // 跨午夜(DeepSeek 错峰)
		{"00:00-08:30", 0, 8*60 + 30, false},
		{"00:00-24:00", 0, 1440, false}, // 24:00 作为终点合法
		{"23:00-24:00", 23 * 60, 1440, false},
		{"9:05-10:00", 9*60 + 5, 600, false}, // 时个位容忍
	}
	for _, c := range cases {
		s, e, err := ParseWindowMinutes(c.in)
		if err != nil || s != c.start || e != c.end {
			t.Fatalf("ParseWindowMinutes(%q) = %d,%d %v; 期望 %d,%d", c.in, s, e, err, c.start, c.end)
		}
	}
}

func TestParseWindowMinutes_非法(t *testing.T) {
	cases := []string{
		"", "16:30", "16:30-", "-00:30", "16:30-00:30-08:00",
		"24:00-01:00",                // 起点不得为 24:00
		"08:00-08:00",                // 空窗
		"25:00-26:00", "08:60-09:00", // 越界
	}
	for _, in := range cases {
		if _, _, err := ParseWindowMinutes(in); err == nil {
			t.Fatalf("ParseWindowMinutes(%q) 应报错", in)
		}
	}
}

func TestParseZoneOffset(t *testing.T) {
	loc, err := ParseZoneOffset("UTC")
	if err != nil || loc.String() != "UTC" {
		t.Fatalf("UTC 解析: %v %v", loc, err)
	}
	loc, err = ParseZoneOffset("+08:00")
	if err != nil {
		t.Fatal(err)
	}
	if _, offset := time.Date(2026, 1, 1, 0, 0, 0, 0, loc).Zone(); offset != 8*3600 {
		t.Fatalf("+08:00 偏移不符: %d", offset)
	}
	loc, err = ParseZoneOffset("-05:30")
	if err != nil {
		t.Fatal(err)
	}
	if _, offset := time.Date(2026, 1, 1, 0, 0, 0, 0, loc).Zone(); offset != -5*3600-1800 {
		t.Fatalf("-05:30 偏移不符: %d", offset)
	}
	for _, in := range []string{"", "Asia/Shanghai", "+8", "08:00", "+25:00", "UTC+8"} {
		if _, err := ParseZoneOffset(in); err == nil {
			t.Fatalf("ParseZoneOffset(%q) 应报错", in)
		}
	}
}

func TestScalePrice(t *testing.T) {
	cases := []struct {
		name      string
		micro, bp int64
		want      int64
	}{
		{"半价恰除尽", 440_000, 5000, 220_000},
		{"零价缩放仍零(cached 缺省语义保持)", 0, 5000, 0},
		{"奇数基向上取整(保守多估 1μ)", 3, 5000, 2}, // 3×0.5=1.5 → ceil 2
		{"1 基点向上取整", 1, 1, 1},            // 0.0001 → ceil 1
		{"原价", 440_000, 10000, 440_000},
	}
	for _, c := range cases {
		got, err := ScalePrice(c.micro, c.bp)
		if err != nil || got != c.want {
			t.Fatalf("%s: ScalePrice(%d,%d) = %d, %v; 期望 %d", c.name, c.micro, c.bp, got, err, c.want)
		}
	}
	if _, err := ScalePrice(math.MaxInt64, 10000+1); err == nil {
		t.Fatal("溢出必须报错")
	}
}

func TestPricingTable_At(t *testing.T) {
	def := Pricing{Currency: "USD", InputCreditsPerM: 440_000, OutputCreditsPerM: 1_320_000}
	utc, _ := ParseZoneOffset("UTC")

	t.Run("无窗口恒缺省", func(t *testing.T) {
		tbl, err := NewPricingTable(def, utc, nil)
		if err != nil {
			t.Fatal(err)
		}
		now := time.Date(2026, 9, 11, 3, 0, 0, 0, time.UTC)
		if got := tbl.At(now); got != def {
			t.Fatalf("无窗口应恒缺省: %+v", got)
		}
	})

	t.Run("跨午夜窗口与边界", func(t *testing.T) {
		tbl, err := NewPricingTable(def, utc, []WindowSpec{
			{StartMin: 16*60 + 30, EndMin: 30, BasisPoints: 5000}, // 16:30-00:30 半价
		})
		if err != nil {
			t.Fatal(err)
		}
		cases := []struct {
			h, m    int
			halfOff bool
		}{
			{16, 29, false}, // 窗口前一刻
			{16, 30, true},  // 起点含
			{23, 59, true},
			{0, 0, true},
			{0, 29, true},
			{0, 30, false}, // 终点不含
			{12, 0, false},
		}
		for _, c := range cases {
			now := time.Date(2026, 9, 11, c.h, c.m, 0, 0, time.UTC)
			got := tbl.At(now)
			wantIn := def.InputCreditsPerM / 2
			if c.halfOff && got.InputCreditsPerM != wantIn {
				t.Fatalf("%02d:%02d 应半价: %+v", c.h, c.m, got)
			}
			if !c.halfOff && got != def {
				t.Fatalf("%02d:%02d 应缺省价: %+v", c.h, c.m, got)
			}
		}
	})

	t.Run("先声明先匹配", func(t *testing.T) {
		tbl, err := NewPricingTable(def, utc, []WindowSpec{
			{StartMin: 0, EndMin: 1440, BasisPoints: 8000}, // 全天 0.8(先声明,赢)
			{StartMin: 0, EndMin: 1440, BasisPoints: 5000}, // 全天 0.5(被遮蔽)
		})
		if err != nil {
			t.Fatal(err)
		}
		now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
		if got := tbl.At(now); got.InputCreditsPerM != 440_000*8/10 {
			t.Fatalf("重叠窗应先声明者赢: %+v", got)
		}
	})

	t.Run("固定偏移时区换算", func(t *testing.T) {
		// 北京时间 00:30-08:30 错峰:UTC 16:30-00:30
		cst, _ := ParseZoneOffset("+08:00")
		tbl, err := NewPricingTable(def, cst, []WindowSpec{
			{StartMin: 30, EndMin: 8*60 + 30, BasisPoints: 5000},
		})
		if err != nil {
			t.Fatal(err)
		}
		// UTC 16:30 = 北京 00:30 → 命中
		if got := tbl.At(time.Date(2026, 9, 11, 16, 30, 0, 0, time.UTC)); got.InputCreditsPerM != 220_000 {
			t.Fatalf("UTC16:30(北京00:30)应半价: %+v", got)
		}
		// UTC 00:30 = 北京 08:30 → 不命中(终点不含)
		if got := tbl.At(time.Date(2026, 9, 11, 0, 30, 0, 0, time.UTC)); got != def {
			t.Fatalf("UTC00:30(北京08:30)应缺省价: %+v", got)
		}
	})

	t.Run("倍率作用于三段价且 cached 缺省语义保持", func(t *testing.T) {
		// def 的 cached=0 表示「缺省=input」:缩放后仍为 0,CostWithCached 的缺省解析不受影响
		defWithCached := Pricing{Currency: "CNY", InputCreditsPerM: 999_999, CachedInputCreditsPerM: 0, OutputCreditsPerM: 3}
		tbl, err := NewPricingTable(defWithCached, utc, []WindowSpec{
			{StartMin: 0, EndMin: 1440, BasisPoints: 5000},
		})
		if err != nil {
			t.Fatal(err)
		}
		got := tbl.At(time.Date(2026, 9, 11, 1, 0, 0, 0, time.UTC))
		if got.InputCreditsPerM != 500_000 || got.CachedInputCreditsPerM != 0 || got.OutputCreditsPerM != 2 {
			t.Fatalf("窗口三段缩放不符(999999×0.5 ceil=500000, 3×0.5 ceil=2): %+v", got)
		}
	})
}

func TestFormatPrice(t *testing.T) {
	cases := []struct {
		credits int64
		want    string
	}{
		{440_000_000, "0.44"},
		{0, "0"},
		{1, "0.000000001"},
		{1_000, "0.000001"},
		{1_000_000_000, "1"},
		{14_000_000, "0.014"},
		{123_456_123_456_000, "123456.123456"},
		{-440_000_000, "-0.44"},
	}
	for _, c := range cases {
		if got := FormatPrice(c.credits); got != c.want {
			t.Fatalf("FormatPrice(%d) = %q, 期望 %q", c.credits, got, c.want)
		}
	}
	// 恒等往返:FormatPrice(ParsePrice(x)) == x(规范化形态)
	for _, in := range []string{"0.44", "0.014", "2", "0.000001", "0.00000001"} {
		credits, err := ParsePrice(in)
		if err != nil {
			t.Fatal(err)
		}
		if got := FormatPrice(credits); got != in {
			t.Fatalf("往返不符: %s → %d → %s", in, credits, got)
		}
	}
}

func TestFormatMultiplier(t *testing.T) {
	cases := []struct {
		bp   int64
		want string
	}{
		{5000, "0.5"},
		{10000, "1"},
		{1, "0.0001"},
		{1234, "0.1234"},
		{100000, "10"},
	}
	for _, c := range cases {
		if got := FormatMultiplier(c.bp); got != c.want {
			t.Fatalf("FormatMultiplier(%d) = %q, 期望 %q", c.bp, got, c.want)
		}
	}
	for _, in := range []string{"0.5", "1", "0.0001", "10"} {
		bp, err := ParseMultiplier(in)
		if err != nil {
			t.Fatal(err)
		}
		if got := FormatMultiplier(bp); got != in {
			t.Fatalf("往返不符: %s → %d → %s", in, bp, got)
		}
	}
}

func TestPricingDirectory(t *testing.T) {
	def := Pricing{Currency: "CNY", InputCreditsPerM: 100}
	tbl, err := NewPricingTable(def, time.UTC, nil)
	if err != nil {
		t.Fatal(err)
	}
	d := NewPricingDirectory(map[string]*PricingTable{"a": tbl})
	if got := d.Get("a"); got == nil || got.At(time.Now()) != def {
		t.Fatalf("Get 应命中: %+v", got)
	}
	if d.Get("missing") != nil {
		t.Fatal("未配置别名应返回 nil")
	}
	// 整体替换原子生效
	tbl2, _ := NewPricingTable(Pricing{Currency: "CNY", InputCreditsPerM: 200}, time.UTC, nil)
	d.Replace(map[string]*PricingTable{"b": tbl2})
	if d.Get("a") != nil || d.Get("b") == nil {
		t.Fatal("Replace 后旧表应消失、新表生效")
	}
	if got := d.Aliases(); len(got) != 1 || got[0] != "b" {
		t.Fatalf("Aliases 不符: %v", got)
	}
}
