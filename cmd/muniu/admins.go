package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/solvela/muniu/internal/config"
	"github.com/solvela/muniu/internal/store"
)

func adminsSubArgs(args []string) ([]string, bool) {
	for i := 1; i < len(args); i++ {
		switch {
		case args[i] == "-c":
			i++
		case args[i] == "admins":
			return args[i+1:], true
		case strings.HasPrefix(args[i], "-"):
		default:
			return nil, false
		}
	}
	return nil, false
}

func runAdmins(path string, args []string) int {
	cfg, err := config.Load(path)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return adminsMain(cfg, args, os.Stdout, os.Stderr)
}

// adminsMain 主体管理 CLI:管理员身份由 Keycloak 签发的 sub 与 client role 决定,本 CLI 只做
// 本地主体投影的查看与启停,不再提供创建/改密(口令已不是登录凭据)。
func adminsMain(cfg *config.Config, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr,
			"用法: muniu admins list | disable <subject> | enable <subject>(管理员须由 Keycloak 签发 sub 且具备 muniu-admin 角色,首次登录自动建档)")
		return 2
	}
	db, err := store.Open(context.Background(), cfg.Storage.SQLite.Path)
	if err != nil {
		return reportErr(stderr, err)
	}
	defer func() { _ = db.Close() }()
	switch args[0] {
	case "list":
		return adminsList(db, stdout, stderr)
	case "disable", "enable":
		return adminsMutate(db, args, stdout, stderr)
	default:
		_, _ = fmt.Fprintln(stderr, "未知 admins 子命令")
		return 2
	}
}

// reportErr 打印错误并以退出码 1 返回。
func reportErr(stderr io.Writer, err error) int {
	_, _ = fmt.Fprintln(stderr, err)
	return 1
}

// adminsList 列出主体:subject 是唯一键,启停命令以它为目标。
func adminsList(db *sql.DB, stdout, stderr io.Writer) int {
	principals, err := store.ListAdminPrincipals(context.Background(), db)
	if err != nil {
		return reportErr(stderr, err)
	}
	for _, p := range principals {
		_, _ = fmt.Fprintf(stdout, "%s\t%s\t%s\n", p.Subject, p.Username, p.Status)
	}
	return 0
}

// adminsMutate 按 subject 启停;停用即撤销该主体全部本地会话。
func adminsMutate(db *sql.DB, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	subject := fs.String("subject", "", "Keycloak subject")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	*subject = strings.TrimSpace(*subject)
	if *subject == "" || fs.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, "须提供 --subject")
		return 2
	}
	disabled := args[0] == "disable"
	if err := store.PatchAdminPrincipal(context.Background(), db, *subject, disabled); err != nil {
		return reportErr(stderr, err)
	}
	_, _ = fmt.Fprintf(stdout, "管理员 %s 已%s\n", *subject, map[bool]string{true: "停用", false: "启用"}[disabled])
	return 0
}
