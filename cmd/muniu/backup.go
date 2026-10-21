// backup 和 restore 子命令管理 SQLite 状态备份。
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/solvela/muniu/internal/config"
	"github.com/solvela/muniu/internal/store"
)

const backupUsage = `muniu backup —— standalone 状态备份与恢复

用法:
  muniu backup [--output DIR]           在线一致快照(运行中可执行,不阻塞读写)
  muniu restore <backup-dir> [--force]  离线恢复(目标实例 MUST 未运行)

说明:
  备份产物 = <DIR>/{muniu.db, manifest.json};manifest 记录版本/迁移版本/
  备份时刻/key 与 usage 行数/sha256(恢复侧校验与对账锚点)。
  目录默认 <data>/backups/muniu-backup-<UTC 时刻>;0700 权限。
  恢复目标缺省为配置的 storage.sqlite.path;已存在需 --force(覆盖前保留
  现场,恢复后复校验,不符自动回滚)。
`

// backupSubArgs 判定是否为 backup/restore 子命令调用并返回其后参数，
// 容忍 -c <path> 出现在子命令前的任意位置。
func backupSubArgs(args []string) ([]string, bool) {
	for i := 1; i < len(args); i++ {
		switch {
		case args[i] == "-c":
			i++
		case args[i] == "backup" || args[i] == "restore":
			return args[i:], true
		case strings.HasPrefix(args[i], "-"):
		default:
			return nil, false
		}
	}
	return nil, false
}

// runBackup 加载配置并执行 backup/restore 子命令，返回进程退出码。
func runBackup(flagConf string, args []string) int {
	cfg, err := config.Load(flagConf)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "muniu: %v\n", err)
		return 1
	}
	return backupMain(cfg, args, os.Stdout, os.Stderr)
}

func backupMain(cfg *config.Config, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, backupUsage)
		return 2
	}
	switch args[0] {
	case "backup":
		return runBackupCmd(cfg, args[1:], stdout, stderr)
	case "restore":
		return runRestoreCmd(cfg, args[1:], stdout, stderr)
	default:
		_, _ = fmt.Fprintf(stderr, "muniu: 未知子命令 %q\n\n%s", args[0], backupUsage)
		return 2
	}
}

// defaultBackupDir 缺省备份目录：<data>/backups/muniu-backup-<UTC 紧凑时刻>。
func defaultBackupDir(dbPath string, now time.Time) string {
	return filepath.Join(filepath.Dir(dbPath), "backups",
		"muniu-backup-"+now.UTC().Format("20060102T150405Z"))
}

func runBackupCmd(cfg *config.Config, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	fs.SetOutput(stderr)
	output := fs.String("output", "", "备份目录(缺省 <data>/backups/muniu-backup-<UTC 时刻>)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		_, _ = fmt.Fprintf(stderr, "muniu backup: 多余参数 %v\n", fs.Args())
		return 2
	}
	db, err := store.Open(cfg.Storage.SQLite.Path)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "muniu backup: %v\n", err)
		return 1
	}
	defer func() { _ = db.Close() }()
	dir := *output
	if dir == "" {
		dir = defaultBackupDir(cfg.Storage.SQLite.Path, time.Now())
	}
	man, err := store.Snapshot(db, dir, time.Now())
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "muniu backup: %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintf(stdout,
		"备份完成: %s\n  muniu=%s migrations=%d taken_at=%s\n  api_keys=%d usage_logs=%d sha256=%s\n",
		dir, man.MuniuVersion, man.MigrationsVersion, man.TakenAtUTC,
		man.APIKeys, man.UsageLogs, man.SHA256)
	return 0
}

func runRestoreCmd(cfg *config.Config, args []string, stdout, stderr io.Writer) int {
	// flag 包在首个位置参数后停止解析，而目录与 --force/--target 混排是自然用法，
	// 故先手工分区再解析；-target 须显式带走一个值，否则其值会被误当位置参数，
	// -- 之后全视为位置参数。
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			pos = append(pos, args[i+1:]...)
			i = len(args)
		case a == "-target" || a == "--target":
			if i+1 >= len(args) {
				_, _ = fmt.Fprintf(stderr, "muniu restore: %s 缺值\n", a)
				return 2
			}
			flags = append(flags, a, args[i+1])
			i++
		case strings.HasPrefix(a, "-"):
			flags = append(flags, a)
		default:
			pos = append(pos, a)
		}
	}
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	fs.SetOutput(stderr)
	force := fs.Bool("force", false, "目标已存在时允许覆盖(覆盖前保留现场,校验不符自动回滚)")
	target := fs.String("target", "", "恢复目标库路径(缺省=配置的 storage.sqlite.path)")
	if err := fs.Parse(flags); err != nil {
		return 2
	}
	if len(pos) != 1 {
		_, _ = fmt.Fprintf(stderr, "muniu restore: 需要一个备份目录参数\n\n%s", backupUsage)
		return 2
	}
	dest := cfg.Storage.SQLite.Path
	if *target != "" {
		dest = *target
	}
	if err := store.Restore(pos[0], dest, *force); err != nil {
		_, _ = fmt.Fprintf(stderr, "muniu restore: %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintf(stdout, "恢复完成: %s → %s\n", pos[0], dest)
	return 0
}
