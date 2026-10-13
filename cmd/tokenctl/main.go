// tokenctl 练手 CLI(Phase 0):复用 internal/metering 的计价换算,顺带预习 flag/标准库用法。
// 子命令:
//
//	cost  —— 按「价目 × token 数」计算成本,各段与合计均以 credit(本币十亿分之一)整数运算后转人类可读;
//	chars —— 统计 stdin 字节/字符/中文字符数,并给出粗估 token(非 BPE,仅供预算参考)。
//
// 金额展示一律由 credit 整数格式化,全程不经 float(conventions「金额整数」铁则)。
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/solvela/muniu/internal/metering"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "cost":
		runCost(os.Args[2:])
	case "chars":
		runChars(os.Stdin)
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "tokenctl: 未知子命令 %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	_, _ = fmt.Fprintf(os.Stderr, `tokenctl — token 计量练手工具

用法:
  tokenctl cost --currency CNY --in-price 2.00 --out-price 8.00 --in 1200 --out 800
  tokenctl chars < 文件
`)
}

// currencySymbol 常见币种的展示符号;未收录币种直接回显代码,避免臆造符号。
func currencySymbol(code string) string {
	switch strings.ToUpper(code) {
	case "CNY":
		return "¥"
	case "USD":
		return "$"
	default:
		return code + " "
	}
}

// formatCredits 与计费契约共用十亿分之一单位，不能再按旧 μ 单位放大金额。
func formatCredits(credits int64) string {
	return fmt.Sprintf("%d.%09d", credits/1_000_000_000, credits%1_000_000_000)
}

func runCost(args []string) {
	fs := flag.NewFlagSet("cost", flag.ExitOnError)
	currency := fs.String("currency", "CNY", "币种代码(CNY/USD 等,决定展示符号)")
	inPrice := fs.String("in-price", "", "输入价(元/百万Token,十进制字串,如 2.00)")
	outPrice := fs.String("out-price", "", "输出价(元/百万Token,十进制字串,如 8.00)")
	inTokens := fs.Int64("in", 0, "输入 token 数")
	outTokens := fs.Int64("out", 0, "输出 token 数")
	_ = fs.Parse(args)

	inMicroPerM, err := metering.ParsePrice(*inPrice)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tokenctl: --in-price %q 不合法(%v)\n", *inPrice, err)
		os.Exit(1)
	}
	outMicroPerM, err := metering.ParsePrice(*outPrice)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tokenctl: --out-price %q 不合法(%v)\n", *outPrice, err)
		os.Exit(1)
	}

	// 分段展示复用 Cost 本身:把另一段 token 置 0 即得该段成本,保证口径与合计完全一致
	inCost := metering.Cost(*inTokens, 0, inMicroPerM, outMicroPerM)
	outCost := metering.Cost(0, *outTokens, inMicroPerM, outMicroPerM)
	total := metering.Cost(*inTokens, *outTokens, inMicroPerM, outMicroPerM)
	if total < 0 {
		fmt.Fprintln(os.Stderr, "tokenctl: token 数非法或计价金额超出 int64 范围")
		os.Exit(1)
	}
	sym := currencySymbol(*currency)

	fmt.Printf("币种:   %s\n", strings.ToUpper(*currency))
	fmt.Printf("输入:   %d token × %s/百万 = %d credit(%s%s)\n", *inTokens, *inPrice, inCost, sym, formatCredits(inCost))
	fmt.Printf("输出:   %d token × %s/百万 = %d credit(%s%s)\n", *outTokens, *outPrice, outCost, sym, formatCredits(outCost))
	fmt.Printf("合计:   %d credit(%s%s)\n", total, sym, formatCredits(total))
}

// runChars 统计 stdin 文本规模并粗估 token。
// 粗估口径(Phase 0 练手级,非 BPE):中文 ≈ 字符数×0.7,英文 ≈ 单词数×1.3;整数运算向上取整。
func runChars(r io.Reader) {
	data, err := io.ReadAll(r)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tokenctl: 读取 stdin 失败(%v)\n", err)
		os.Exit(1)
	}

	han, words := 0, 0
	inWord := false
	for _, c := range string(data) {
		switch {
		case unicode.Is(unicode.Han, c):
			han++
			inWord = false
		case unicode.IsLetter(c) || unicode.IsDigit(c):
			if !inWord {
				words++
			}
			inWord = true
		default:
			inWord = false
		}
	}
	runes := utf8.RuneCount(data)
	// ×0.7 / ×1.3 用整数放大后取整(×7/10、×13/10),不引入 float
	estimate := ceilDiv(int64(han)*7, 10) + ceilDiv(int64(words)*13, 10)

	fmt.Printf("字节数:     %d\n", len(data))
	fmt.Printf("字符数:     %d(其中中文字符 %d)\n", runes, han)
	fmt.Printf("英文单词数: %d\n", words)
	fmt.Printf("粗估 token: ≈ %d\n", estimate)
	fmt.Println("说明:     以上为粗估(中文≈字符数×0.7,英文≈单词数×1.3),非 BPE 精确计数,仅供预算参考。")
}

// ceilDiv 非负整数除法向上取整(与 metering 内部口径一致,CLI 侧不导出复用)。
func ceilDiv(n, d int64) int64 {
	q, rem := n/d, n%d
	if rem > 0 {
		q++
	}
	return q
}
