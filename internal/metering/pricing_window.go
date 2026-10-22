// 分时段倍率按基点解析，并在加载期换算为整数价目，避免热路径浮点运算。
// 每个价格向上取整，避免换算后低估成本。
package metering

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// scaleDenom 倍率分母：1e4 基点，即万分之一，金融标准粒度。
const scaleDenom = 10_000

// maxScaleBp 倍率上限 10 倍；错峰恒为折扣，溢价窗留余量到 10x。
const maxScaleBp = 100_000

// MaxScaleBp 倍率上限，导出供运行态重建断言同域校验。
const MaxScaleBp = maxScaleBp

// ParseMultiplier 人类可读倍率 → 基点，"0.5" 表半价。最多 4 位小数即基点精度，
// 取值 (0, 10]；逐位十进制解析，不经 float，与 ParsePrice 同纪律，哨兵同为 ErrInvalidPrice。
func ParseMultiplier(human string) (int64, error) {
	intPart, fracPart, ok := strings.Cut(human, ".")
	if !ok {
		fracPart = ""
	}
	if intPart == "" || (fracPart == "" && ok) {
		return 0, ErrInvalidPrice // "0.5." / ".5" 拒绝，与 ParsePrice 同形态纪律。
	}
	if !isDigits(intPart) || !isDigits(fracPart) {
		return 0, ErrInvalidPrice
	}
	if len(fracPart) > 4 {
		return 0, ErrInvalidPrice
	}
	intVal, err := strconv.ParseInt(intPart, 10, 64)
	if err != nil {
		return 0, ErrInvalidPrice
	}
	fracVal := int64(0)
	if fracPart != "" {
		fracVal, err = strconv.ParseInt(fracPart+strings.Repeat("0", 4-len(fracPart)), 10, 64)
		if err != nil {
			return 0, ErrInvalidPrice
		}
	}
	// 乘法溢出防护：整数部分超上限直接拒，否则回绕可把非法输入洗成合法倍率。
	if intVal > maxScaleBp/scaleDenom {
		return 0, ErrInvalidPrice
	}
	bp := intVal*scaleDenom + fracVal
	if bp <= 0 || bp > maxScaleBp {
		return 0, ErrInvalidPrice
	}
	return bp, nil
}

// ParseWindowMinutes 解析 "HH:MM-HH:MM" 窗口为当日分钟区间；终点允许 "24:00" 表当日末；
// start > end 即跨午夜窗，如 16:30-00:30。匹配语义为 [start, end) 终点不含；start == end 为空窗，拒绝。
func ParseWindowMinutes(s string) (start, end int, err error) {
	bad := fmt.Errorf("metering: 非法时段窗 %q(须为 HH:MM-HH:MM)", s)
	first, second, ok := strings.Cut(s, "-")
	if !ok {
		return 0, 0, bad
	}
	start, err = parseClockMinutes(first)
	if err != nil || start >= 24*60 {
		return 0, 0, bad // 起点不得为 24:00
	}
	end, err = parseClockMinutes(second)
	if err != nil {
		return 0, 0, bad
	}
	if start == end {
		return 0, 0, bad // 空窗
	}
	return start, end, nil
}

// parseClockMinutes 解析 "HH:MM"，时个位容忍，得当日分钟；支持 24:00(=1440)，仅终点合法，起点校验由调用方做。
func parseClockMinutes(s string) (int, error) {
	hs, ms, ok := strings.Cut(strings.TrimSpace(s), ":")
	if !ok {
		return 0, fmt.Errorf("metering: 非法时刻 %q", s)
	}
	// 纯数字校验先于 Atoi：strconv 接受 +/- 前缀，"+8:30" 这类 typo 会被静默按错误语义计价。
	if !isDigits(hs) || !isDigits(ms) {
		return 0, fmt.Errorf("metering: 非法时刻 %q", s)
	}
	h, err := strconv.Atoi(hs)
	if err != nil {
		return 0, err
	}
	m, err := strconv.Atoi(ms)
	if err != nil {
		return 0, err
	}
	if h < 0 || h > 24 || m < 0 || m > 59 || (h == 24 && m != 0) {
		return 0, fmt.Errorf("metering: 时刻越界 %q", s)
	}
	return h*60 + m, nil
}

// ParseZoneOffset 固定偏移时区："UTC" 或 "+08:00"/"-05:30" 形态。
// 不引 tzdata；DST 影响的时区应换算为 UTC 表达。
func ParseZoneOffset(s string) (*time.Location, error) {
	if s == "UTC" {
		return time.UTC, nil
	}
	sign := ""
	rest := ""
	switch {
	case strings.HasPrefix(s, "+"):
		sign, rest = "+", s[1:]
	case strings.HasPrefix(s, "-"):
		sign, rest = "-", s[1:]
	default:
		return nil, fmt.Errorf("metering: 非法时区 %q(须为 UTC 或 ±HH:MM 固定偏移)", s)
	}
	hs, ms, ok := strings.Cut(rest, ":")
	if !ok {
		return nil, fmt.Errorf("metering: 非法时区 %q(须为 ±HH:MM)", s)
	}
	h, err1 := strconv.Atoi(hs)
	m, err2 := strconv.Atoi(ms)
	if err1 != nil || err2 != nil || len(hs) != 2 || len(ms) != 2 || h > 23 || m > 59 {
		return nil, fmt.Errorf("metering: 非法时区 %q", s)
	}
	off := h*3600 + m*60
	if sign == "-" {
		off = -off
	}
	return time.FixedZone(s, off), nil
}

// ScalePrice 倍率缩放：ceil(credits × bp / 1e4)，向上取整保守计价，窗口价永不低估；
// 溢出防护同 ParsePrice 换算。
func ScalePrice(micro, bp int64) (int64, error) {
	if micro == 0 {
		return 0, nil // 0 即 cached 缺省 input 语义，缩放保持 0，缺省解析不受影响。
	}
	if micro < 0 || bp <= 0 || micro > math.MaxInt64/bp {
		return 0, ErrInvalidPrice
	}
	return ceilDiv(micro*bp, scaleDenom), nil
}

// WindowSpec 时段规格的结构化形态，config 校验后构造。
type WindowSpec struct {
	StartMin    int   // 当日分钟，按配置时区，含起点。
	EndMin      int   // 终点不含；1440=当日末；StartMin > EndMin 即跨午夜。
	BasisPoints int64 // 倍率基点
}

// PricingTable 分时段价目表：缺省三段价 + 倍率窗。多窗按声明顺序先匹配优先，
// 重叠即声明序 precedence，可预期且免跨午夜区间代数；无命中回落缺省价。
type PricingTable struct {
	loc *time.Location
	def Pricing

	windows []scaledWindow
}

type scaledWindow struct {
	spec    WindowSpec
	pricing Pricing // 加载期已按倍率换算的有效三段价
}

// NewPricingTable 构造并预换算全部窗口价目，一次性整数运算；任何一段溢出即整体报错。
func NewPricingTable(def Pricing, zone *time.Location, specs []WindowSpec) (*PricingTable, error) {
	if zone == nil {
		zone = time.UTC
	}
	t := &PricingTable{loc: zone, def: def}
	for _, sp := range specs {
		in, err := ScalePrice(def.InputCreditsPerM, sp.BasisPoints)
		if err != nil {
			return nil, fmt.Errorf("metering: 时段窗 %d-%d input 价缩放: %w", sp.StartMin, sp.EndMin, err)
		}
		cached, err := ScalePrice(def.CachedInputCreditsPerM, sp.BasisPoints)
		if err != nil {
			return nil, fmt.Errorf("metering: 时段窗 %d-%d cached_input 价缩放: %w", sp.StartMin, sp.EndMin, err)
		}
		vision, err := ScalePrice(def.VisionInputCreditsPerM, sp.BasisPoints)
		if err != nil {
			return nil, fmt.Errorf("metering: 时段窗 %d-%d vision_input 价缩放: %w", sp.StartMin, sp.EndMin, err)
		}
		out, err := ScalePrice(def.OutputCreditsPerM, sp.BasisPoints)
		if err != nil {
			return nil, fmt.Errorf("metering: 时段窗 %d-%d output 价缩放: %w", sp.StartMin, sp.EndMin, err)
		}
		t.windows = append(t.windows, scaledWindow{
			spec: sp,
			pricing: Pricing{
				Currency: def.Currency, InputCreditsPerM: in,
				CachedInputCreditsPerM: cached, VisionInputCreditsPerM: vision, OutputCreditsPerM: out,
			},
		})
	}
	return t, nil
}

// At 按时刻选价：窗口判定用配置时区的当日分钟；先声明先匹配；无命中回落缺省。
func (t *PricingTable) At(now time.Time) Pricing {
	if len(t.windows) == 0 {
		return t.def
	}
	lt := now.In(t.loc)
	m := lt.Hour()*60 + lt.Minute()
	for _, w := range t.windows {
		if inWindow(m, w.spec) {
			return w.pricing
		}
	}
	return t.def
}

// inWindow 按 [start,end) 语义判定；start > end 为跨午夜(m ≥ start || m < end)。
func inWindow(m int, spec WindowSpec) bool {
	if spec.StartMin < spec.EndMin {
		return m >= spec.StartMin && m < spec.EndMin
	}
	return m >= spec.StartMin || m < spec.EndMin // 跨午夜
}
