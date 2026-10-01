// 状态备份与恢复(openspec 2026-09-30-state-backup-restore,spec state-backup)。
//
// standalone 形态的全量运营态(key 哈希/用量账本/价目)住在单个 SQLite 文件,
// 本文件给出「一条命令备份、一条命令恢复」的存储侧纯能力:
//
//   - Snapshot:VACUUM INTO 在线一致快照(WAL 下不阻塞读写)+ 完整性自检 +
//     manifest(版本/迁移版本/时刻/行数/sha256);任一步失败删档不残留。
//   - Restore:三道前置闸(快照自校验 → 目标占用探测 → 覆盖需 force),
//     原子落位(临时文件 + rename,覆盖前保留现场),恢复后再校验,不符回滚。
//
// 恢复定位是**离线运维动作**(停服 → 恢复 → 起服):目标库被实例持锁时拒绝,
// 不做在线热切换——单文件库在线替换是与自己竞态(design D3)。
package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/solvela/muniu/internal/version"
)

// backupDBName 快照库文件名(与线上库同名,恢复时直替 data/muniu.db)。
const backupDBName = "muniu.db"

// manifestName 备份自描述文件名。
const manifestName = "manifest.json"

// ErrLocked 目标库被其他进程持锁(实例在运行)——Restore 前置闸之一。
var ErrLocked = errors.New("store: 目标库被占用(实例可能在运行)")

// Manifest 备份自描述:恢复侧校验锚点 + 对账分母(usage_logs 行数)。
type Manifest struct {
	MuniuVersion      string `json:"muniu_version"`
	MigrationsVersion int    `json:"migrations_version"`
	TakenAtUTC        string `json:"taken_at_utc"`
	APIKeys           int    `json:"api_keys"`
	UsageLogs         int    `json:"usage_logs"`
	SHA256            string `json:"sha256"`
}

// Snapshot 对 db(运行中实例或 CLI 重开的同文件连接)取一致快照落到 dir。
// dir 必须不存在(防误混入既有备份);快照 = dir/muniu.db + dir/manifest.json。
func Snapshot(db *sql.DB, dir string, now time.Time) (Manifest, error) {
	if _, err := os.Stat(dir); err == nil {
		return Manifest{}, fmt.Errorf("store: 备份目录已存在(换一个 --output): %s", dir)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil { // 备份含 key 哈希与账本,0700
		return Manifest{}, fmt.Errorf("store: 创建备份目录: %w", err)
	}
	snap := filepath.Join(dir, backupDBName)
	if _, err := os.Stat(snap); err == nil {
		_ = os.RemoveAll(dir)
		return Manifest{}, fmt.Errorf("store: 快照目标已存在: %s", snap)
	}
	// VACUUM INTO:单语句级读事务,WAL 下取一致快照,不阻塞读写。路径以 SQL
	// 字面量转义注入(该语句不接受参数绑定;本地运维路径,信任面内)。
	escaped := strings.ReplaceAll(snap, "'", "''")
	if _, err := db.Exec(fmt.Sprintf("VACUUM INTO '%s'", escaped)); err != nil {
		_ = os.RemoveAll(dir) // 坏档不残留(半成品备份比没有备份更危险)
		return Manifest{}, fmt.Errorf("store: 快照失败: %w", err)
	}
	man, err := verifySnapshot(snap, now)
	if err != nil {
		_ = os.RemoveAll(dir)
		return Manifest{}, err // 已包 %w,不再裹
	}
	if err := writeManifest(dir, man); err != nil {
		_ = os.RemoveAll(dir)
		return Manifest{}, err
	}
	return man, nil
}

// verifySnapshot 打开快照独立校验:integrity_check 必须 ok;行数与迁移版本
// 入 manifest(sha256 覆盖快照文件本体,防静默损坏)。
func verifySnapshot(snap string, now time.Time) (Manifest, error) {
	db, err := openPlain(snap)
	if err != nil {
		return Manifest{}, fmt.Errorf("store: 打开快照校验: %w", err)
	}
	defer func() { _ = db.Close() }()
	var integrity string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil {
		return Manifest{}, fmt.Errorf("store: 完整性自检执行失败: %w", err)
	}
	if integrity != "ok" {
		return Manifest{}, fmt.Errorf("store: 快照完整性自检未过: %s", integrity)
	}
	man := Manifest{
		MuniuVersion: version.Version,
		TakenAtUTC:   now.UTC().Format(time.RFC3339),
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM api_keys").Scan(&man.APIKeys); err != nil {
		return Manifest{}, fmt.Errorf("store: 统计 api_keys: %w", err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM usage_logs").Scan(&man.UsageLogs); err != nil {
		return Manifest{}, fmt.Errorf("store: 统计 usage_logs: %w", err)
	}
	if err := db.QueryRow(
		"SELECT COALESCE(MAX(version), 0) FROM schema_migrations",
	).Scan(&man.MigrationsVersion); err != nil {
		return Manifest{}, fmt.Errorf("store: 读取迁移版本: %w", err)
	}
	sum, err := fileSHA256(snap)
	if err != nil {
		return Manifest{}, err
	}
	man.SHA256 = sum
	return man, nil
}

// Restore 把 dir 中的备份恢复到 destPath(通常是配置里的 storage.sqlite.path)。
// 顺序即 spec 前置闸:先校验快照本体(坏档在触碰目标前拒),再探测目标占用,
// 覆盖需 force;原子落位,恢复后复校验,不符回滚现场。
func Restore(dir, destPath string, force bool) error {
	man, err := readManifest(dir)
	if err != nil {
		return err
	}
	snap := filepath.Join(dir, backupDBName)
	if _, err := os.Stat(snap); err != nil {
		return fmt.Errorf("store: 备份缺快照文件: %w", err)
	}
	// 闸零:快照文件本体哈希与 manifest 一致(防备份被静默替换/损坏)
	sum, err := fileSHA256(snap)
	if err != nil {
		return err
	}
	if sum != man.SHA256 {
		return fmt.Errorf("store: 快照 sha256 与 manifest 不符(备份损坏或被改动)")
	}
	// 闸一(前置):快照本体自校验——integrity + 行数与 manifest 一致,
	// 坏档在触碰目标前拒
	if _, err := verifySnapshot(snap, time.Now().UTC()); err != nil {
		return err
	}
	if got, err := snapshotCounts(snap); err != nil {
		return err
	} else if got.APIKeys != man.APIKeys || got.UsageLogs != man.UsageLogs {
		return fmt.Errorf(
			"store: 备份行数与 manifest 不符(keys %d/%d, usage %d/%d)",
			got.APIKeys, man.APIKeys, got.UsageLogs, man.UsageLogs,
		)
	}
	// 闸二:目标占用探测(SQLite 连接持有期 db 与 -shm 均带 POSIX 锁)
	if _, err := os.Stat(destPath); err == nil {
		if err := probeLocked(destPath); err != nil {
			return err
		}
		if !force {
			return fmt.Errorf("store: 目标已存在(需 --force 覆盖): %s", destPath)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("store: 探测目标路径: %w", err)
	}
	// 原子落位:临时文件 + rename;覆盖场景保留现场(preRestore),复校验
	// 失败即回滚(design D5 失败原子性)。
	// 覆盖时主库与其 -wal/-shm 三件套**整体**换名: unclean shutdown 残留的
	// 旧 WAL 帧只属于旧库,单独替换主库文件会把陈旧 WAL 留给新库——下次
	// 打开时旧帧经恢复叠到新库上(静默数据混杂,已由回归用例钉死)。
	sidecars := []string{"", "-wal", "-shm"}
	tmp := destPath + ".restore-tmp"
	if err := copyFile(snap, tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("store: 写入恢复临时文件: %w", err)
	}
	preRestore := destPath + ".pre-restore"
	hadDest := false
	var moved []string // 已换名成功的后缀(回滚用)
	if _, err := os.Stat(destPath); err == nil {
		hadDest = true
		for _, s := range sidecars {
			if _, err := os.Stat(destPath + s); err != nil {
				continue // 不存在(如无 -wal)跳过
			}
			if err := os.Rename(destPath+s, preRestore+s); err != nil {
				for _, m := range moved {
					_ = os.Rename(preRestore+m, destPath+m)
				}
				_ = os.Remove(tmp)
				return fmt.Errorf("store: 保留现场失败: %w", err)
			}
			moved = append(moved, s)
		}
	}
	if err := os.Rename(tmp, destPath); err != nil {
		if hadDest {
			for _, m := range moved {
				_ = os.Rename(preRestore+m, destPath+m) // 回滚现场
			}
		}
		_ = os.Remove(tmp)
		return fmt.Errorf("store: 落位恢复文件: %w", err)
	}
	// 恢复后再校验(spec「恢复后 integrity_check + 行数核对」)
	if _, err := verifySnapshot(destPath, time.Now().UTC()); err != nil {
		for _, s := range sidecars {
			_ = os.Remove(destPath + s)
		}
		if hadDest {
			for _, m := range moved {
				_ = os.Rename(preRestore+m, destPath+m) // 回滚
			}
		}
		return fmt.Errorf("store: 恢复后校验未过,已回滚现场: %w", err)
	}
	if hadDest {
		for _, m := range moved {
			_ = os.Remove(preRestore + m)
		}
	}
	return nil
}

// probeLocked fcntl 全文件写锁探测:SQLite 连接持有期(db 与 -shm)带 POSIX
// 范围锁(实测:空闲连接亦然,进程退出即释放),写锁必然冲突——即「实例在跑」。
// 仅 Unix(发布目标 linux/darwin;goreleaser 无 windows)。
func probeLocked(path string) error {
	for _, p := range []string{path, path + "-shm"} {
		f, err := os.OpenFile(p, os.O_RDWR, 0)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return fmt.Errorf("store: 打开占用探测目标: %w", err)
		}
		lock := syscall.Flock_t{Type: syscall.F_WRLCK, Whence: io.SeekStart, Start: 0, Len: 0}
		err = syscall.FcntlFlock(f.Fd(), syscall.F_SETLK, &lock)
		_ = f.Close()
		if err != nil {
			return ErrLocked
		}
	}
	return nil
}

type counts struct {
	APIKeys   int
	UsageLogs int
}

// snapshotCounts 复算快照行数(与 readManifest 的值比对)。
func snapshotCounts(snap string) (counts, error) {
	db, err := openPlain(snap)
	if err != nil {
		return counts{}, fmt.Errorf("store: 打开快照统计: %w", err)
	}
	defer func() { _ = db.Close() }()
	var c counts
	if err := db.QueryRow("SELECT COUNT(*) FROM api_keys").Scan(&c.APIKeys); err != nil {
		return counts{}, fmt.Errorf("store: 统计 api_keys: %w", err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM usage_logs").Scan(&c.UsageLogs); err != nil {
		return counts{}, fmt.Errorf("store: 统计 usage_logs: %w", err)
	}
	return c, nil
}

func readManifest(dir string) (Manifest, error) {
	raw, err := os.ReadFile(filepath.Join(dir, manifestName))
	if err != nil {
		return Manifest{}, fmt.Errorf("store: 读取 manifest(非 muniu 备份目录?): %w", err)
	}
	var man Manifest
	if err := json.Unmarshal(raw, &man); err != nil {
		return Manifest{}, fmt.Errorf("store: manifest 解析失败(备份损坏): %w", err)
	}
	if man.SHA256 == "" {
		return Manifest{}, fmt.Errorf("store: manifest 缺 sha256(备份损坏)")
	}
	return man, nil
}

func writeManifest(dir string, man Manifest) error {
	raw, err := json.MarshalIndent(man, "", "  ")
	if err != nil {
		return fmt.Errorf("store: 序列化 manifest: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, manifestName), raw, 0o600); err != nil {
		return fmt.Errorf("store: 写 manifest: %w", err)
	}
	return nil
}

// openPlain 以最裸形态打开快照(只读校验用途;不走 Open 的 pragma/迁移)。
func openPlain(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("store: 打开快照算哈希: %w", err)
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("store: 计算快照哈希: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func copyFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
