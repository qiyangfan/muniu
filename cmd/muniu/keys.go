// keys 管理子命令(单二进制形态,Phase 4 才有管理 API):
//
//	muniu keys create --tenant default --name kongming [--rpm 120]   # 明文仅此一次展示
//	muniu keys list
//	muniu keys revoke <prefix>
//
// 数据库路径取自配置(与网关同一 store)。
package main

import (
	"database/sql"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/solvela/muniu/internal/auth"
	"github.com/solvela/muniu/internal/config"
	"github.com/solvela/muniu/internal/store"
)

// keysMain keys 子命令入口;返回进程退出码。cfg 由调用方加载(与网关共享配置定位)。
func keysMain(cfg *config.Config, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, keysUsage)
		return 2
	}
	db, err := store.Open(cfg.Storage.SQLite.Path)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "muniu keys: 打开存储失败: %v\n", err)
		return 1
	}
	defer func() { _ = db.Close() }()

	switch args[0] {
	case "create":
		return keysCreate(db, args[1:], stdout, stderr)
	case "list":
		return keysList(db, stdout, stderr)
	case "revoke":
		return keysRevoke(db, args[1:], stdout, stderr)
	case "disable":
		return keysToggle(db, args[1:], true, stdout, stderr)
	case "enable":
		return keysToggle(db, args[1:], false, stdout, stderr)
	default:
		_, _ = fmt.Fprintf(stderr, "muniu keys: 未知子命令 %q\n%s", args[0], keysUsage)
		return 2
	}
}

const keysUsage = `用法:
  muniu keys create --tenant default --name kongming [--rpm 120] [--tpm 0] [--daily-quota-credits 0] [--expires-at RFC3339]
  muniu keys list
  muniu keys revoke <prefix>
  muniu keys disable <prefix>
  muniu keys enable <prefix>
`

// keysCreate 签发:明文只打印一次,stderr 同时给出警示。
// 限额与过期字段暴露存储层既有能力(tpm/日配额列本就生效于限流与配额闸;过期迁移 0006)。
func keysCreate(db *sql.DB, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("create", flag.ContinueOnError)
	fs.SetOutput(stderr)
	tenant := fs.String("tenant", "default", "租户名(缺省 default)")
	name := fs.String("name", "", "key 名称(必填)")
	rpm := fs.Int64("rpm", 60, "RPM 限额")
	tpm := fs.Int64("tpm", 0, "TPM 限额(0=不限)")
	quota := fs.Int64("daily-quota-credits", 0, "日配额 credit(1 credit = 十亿分之一本币;0=不限)")
	reqBudget := fs.Int64("max-request-cost-credits", 0, "单请求成本预算 credit(0=不限)")
	expires := fs.String("expires-at", "", "过期时刻 RFC3339(空=永不过期)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *name == "" {
		_, _ = fmt.Fprintln(stderr, "muniu keys create: --name 必填")
		return 2
	}
	opts := auth.KeyOpts{TPMLimit: *tpm, DailyQuotaCredits: *quota, MaxRequestCostCredits: *reqBudget}
	if *expires != "" {
		t, err := time.Parse(time.RFC3339, *expires)
		if err != nil {
			_, _ = fmt.Fprintln(stderr, "muniu keys create: --expires-at 须为 RFC3339 形态")
			return 2
		}
		opts.ExpiresAt = &t
	}
	plaintext, rec, err := auth.CreateKey(db, *tenant, *name, *rpm, opts)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "muniu keys create: %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintln(stderr, "!! 明文仅此一次展示,请立即保存;库中只存 sha256。")
	exp := "永不过期"
	if rec.ExpiresAt != nil {
		exp = rec.ExpiresAt.UTC().Format(time.RFC3339)
	}
	_, _ = fmt.Fprintf(stdout, "key:      %s\nprefix:   %s\nname:     %s\ntenant:   %s\nrpm:      %d\ntpm:      %d\nquota:    %d credit/日\nbudget:   %d credit/请求\nexpires:  %s\n",
		plaintext, rec.Prefix, rec.Name, *tenant, rec.RPMLimit, rec.TPMLimit, rec.DailyQuotaCredits, rec.MaxRequestCostCredits, exp)
	return 0
}

// keysList 列出全部 key(不含任何明文/哈希)。
func keysList(db *sql.DB, stdout, stderr io.Writer) int {
	keys, err := auth.ListKeys(db)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "muniu keys list: %v\n", err)
		return 1
	}
	if len(keys) == 0 {
		_, _ = fmt.Fprintln(stdout, "(无 key)")
		return 0
	}
	_, _ = fmt.Fprintf(stdout, "%-14s %-16s %-10s %-6s %-9s %-12s %s\n", "PREFIX", "NAME", "TENANT", "RPM", "STATUS", "REQ-BUDGET", "ID")
	for _, k := range keys {
		status := k.Status
		if status == "active" && k.Disabled {
			status = "disabled"
		}
		_, _ = fmt.Fprintf(stdout, "%-14s %-16s %-10s %-6d %-9s %-12d %s\n", k.Prefix, k.Name, k.TenantID, k.RPMLimit, status, k.MaxRequestCostCredits, k.ID)
	}
	return 0
}

// keysRevoke 按 prefix 吊销。
func keysRevoke(db *sql.DB, args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		_, _ = fmt.Fprintln(stderr, "muniu keys revoke: 需要一个 <prefix> 参数")
		return 2
	}
	n, err := auth.RevokeKeyByPrefix(db, args[0])
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "muniu keys revoke: %v\n", err)
		return 1
	}
	if n == 0 {
		_, _ = fmt.Fprintf(stdout, "未找到 active 状态的 prefix %s(可能已吊销或不存在)\n", args[0])
		return 1
	}
	// CLI 与网关是两个进程,无法直清网关的鉴权缓存:如实说明收敛时长,不承诺「立即失效」
	_, _ = fmt.Fprintf(stdout, "已吊销 %d 把 key(prefix=%s);运行中网关的鉴权缓存至多 %s 内收敛。\n",
		n, args[0], (10 * time.Second).String())
	return 0
}

// keysToggle 启停 key(可逆禁用):按 prefix 定位后 PATCH disabled_at 列。
// CLI 与网关是两个进程,无法直清网关的鉴权缓存:如实说明收敛时长(revoke 同款文案纪律)。
func keysToggle(db *sql.DB, args []string, disable bool, stdout, stderr io.Writer) int {
	verb := "enable"
	if disable {
		verb = "disable"
	}
	if len(args) != 1 {
		_, _ = fmt.Fprintf(stderr, "muniu keys %s: 需要一个 <prefix> 参数\n", verb)
		return 2
	}
	keys, err := auth.ListKeys(db)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "muniu keys %s: %v\n", verb, err)
		return 1
	}
	var target *auth.KeyRecord
	for i := range keys {
		if keys[i].Prefix == args[0] {
			target = &keys[i]
			break
		}
	}
	if target == nil {
		_, _ = fmt.Fprintf(stdout, "未找到 prefix %s 的 key\n", args[0])
		return 1
	}
	if target.Status != "active" {
		_, _ = fmt.Fprintf(stdout, "key %s 已吊销(终态),启停操作不改变其语义\n", args[0])
		return 1
	}
	v := disable
	rec, err := auth.UpdateKeyLimits(db, target.ID, auth.LimitsPatch{Disabled: &v})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "muniu keys %s: %v\n", verb, err)
		return 1
	}
	action := "已启用"
	if rec.Disabled {
		action = "已停用"
	}
	_, _ = fmt.Fprintf(stdout, "%s key(prefix=%s);运行中网关的鉴权缓存至多 %s 内收敛。\n",
		action, rec.Prefix, (10 * time.Second).String())
	return 0
}

// keysSubArgs 判定是否为 keys 子命令调用,并返回 keys 之后的参数
// (容忍 -c <path> 出现在子命令前的任意位置;非 keys 调用返回 false)。
func keysSubArgs(args []string) ([]string, bool) {
	for i := 1; i < len(args); i++ {
		switch {
		case args[i] == "-c":
			i++ // 跳过其参数值
		case args[i] == "keys":
			return args[i+1:], true
		case strings.HasPrefix(args[i], "-"):
			// 其他全局 flag:继续扫
		default:
			return nil, false // 首个位置参数即子命令,不是 keys
		}
	}
	return nil, false
}

// runKeys 加载配置并执行 keys 子命令(进程退出码语义)。
func runKeys(flagConf string, args []string) int {
	cfg, err := config.Load(flagConf)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "muniu keys: %v\n", err)
		return 1
	}
	return keysMain(cfg, args, os.Stdout, os.Stderr)
}
