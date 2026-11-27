// muniu-breakglass：独立运行时存储修复 CLI。独立凭证、限时窗口、原因、二次确认与审计；
// 仅支持文档化修复操作，不接受任意 SQL，不嵌入控制台、不提供 admin 端点。
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/solvela/muniu/internal/breakglass"
)

type cliFlags struct {
	dbPath    string
	credPath  string
	op        string
	paramsRaw string
	reason    string
	maxRows   int
	yes       bool
	list      bool
}

func main() {
	os.Exit(run(os.Args[1:]))
}

// run 解析参数、列表或执行。
func run(args []string) int {
	f, code := parseFlags(args)
	if code != 0 {
		return code
	}
	if f.list {
		for _, op := range breakglass.Operations() {
			fmt.Printf("%-28s %s\n", op.Name, op.Description)
		}
		return 0
	}
	return execute(f)
}

// parseFlags 解析命令行；错误返回非零。
func parseFlags(args []string) (*cliFlags, int) {
	f := &cliFlags{}
	fs := flag.NewFlagSet("muniu-breakglass", flag.ContinueOnError)
	fs.StringVar(&f.dbPath, "db", "", "运行时 SQLite 路径")
	fs.StringVar(&f.credPath, "credential", "", "独立凭证文件路径（0600）")
	fs.StringVar(&f.op, "op", "", "修复操作名；-list 查看")
	fs.StringVar(&f.paramsRaw, "params", "{}", "操作参数 JSON")
	fs.StringVar(&f.reason, "reason", "", "修复原因（审计留痕）")
	fs.IntVar(&f.maxRows, "max-rows", 100, "影响行数上限")
	fs.BoolVar(&f.yes, "yes", false, "跳过交互确认（仍等同二次确认）")
	fs.BoolVar(&f.list, "list", false, "列出文档化修复操作")
	if err := fs.Parse(args); err != nil {
		return nil, 2
	}
	return f, 0
}

// execute 校验必填项、构造确认回调并调用 breakglass。
func execute(f *cliFlags) int {
	if f.dbPath == "" || f.credPath == "" || f.op == "" || f.reason == "" {
		fmt.Fprintln(os.Stderr, "用法：MUNIU_BREAK_GLASS_TOKEN=... muniu-breakglass "+
			"-db <path> -credential <file> -op <op> -reason <reason> "+
			"[-params JSON] [-max-rows N] [-yes]")
		return 2
	}
	var params map[string]any
	if err := json.Unmarshal([]byte(f.paramsRaw), &params); err != nil {
		fmt.Fprintf(os.Stderr, "params 非法: %v\n", err)
		return 2
	}
	report, err := breakglass.Run(context.Background(), breakglass.Options{
		DBPath: f.dbPath, CredentialPath: f.credPath,
		Token: os.Getenv("MUNIU_BREAK_GLASS_TOKEN"), Op: f.op, Reason: f.reason,
		Params: params, MaxRows: f.maxRows, Confirm: confirmFunc(f),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "break-glass 失败: %v\n", err)
		return 2
	}
	fmt.Printf("已完成 %s：影响 %d 行；审计 %s\n", report.Op, report.Rows, report.AuditID)
	return 0
}

// confirmFunc 交互或 -yes 二次确认。
func confirmFunc(f *cliFlags) func(op, description string) (bool, error) {
	return func(name, description string) (bool, error) {
		fmt.Fprintf(os.Stderr, "即将执行 %s：%s\n原因：%s\n", name, description, f.reason)
		if f.yes {
			return true, nil
		}
		fmt.Fprint(os.Stderr, "输入 yes 确认：")
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil {
			return false, err
		}
		return strings.EqualFold(strings.TrimSpace(line), "yes"), nil
	}
}
