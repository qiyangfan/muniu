// SQLite 状态备份使用 VACUUM INTO 生成一致快照，并记录校验清单。
// 恢复前校验快照、检查目标占用；覆盖需显式授权，并在恢复后复校验，失败时回滚。
// 恢复必须离线执行，目标数据库被实例占用时会拒绝。
package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/solvela/muniu/internal/version"
)

// backupDBName 是快照数据库文件名。
const backupDBName = "muniu.db"

// manifestName 是备份清单文件名。
const manifestName = "manifest.json"

// ErrLocked 表示目标数据库被其他进程占用。
var ErrLocked = errors.New("store: 目标库被占用(实例可能在运行)")

// Manifest 保存备份版本、行数和文件校验值。
type Manifest struct {
	MuniuVersion      string `json:"muniu_version"`
	MigrationsVersion int    `json:"migrations_version"`
	TakenAtUTC        string `json:"taken_at_utc"`
	APIKeys           int    `json:"api_keys"`
	UsageLogs         int    `json:"usage_logs"`
	SHA256            string `json:"sha256"`
}

// Snapshot 将 db 的一致快照和清单写入新目录 dir；目录必须不存在。
func Snapshot(ctx context.Context, db *sql.DB, dir string, now time.Time) (Manifest, error) {
	if _, err := os.Stat(dir); err == nil {
		return Manifest{}, fmt.Errorf("store: 备份目录已存在(换一个 --output): %s", dir)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil { // 备份含 key 哈希与账本，用 0700。
		return Manifest{}, fmt.Errorf("store: 创建备份目录: %w", err)
	}
	snap := filepath.Join(dir, backupDBName)
	if _, err := os.Stat(snap); err == nil {
		_ = os.RemoveAll(dir)
		return Manifest{}, fmt.Errorf("store: 快照目标已存在: %s", snap)
	}
	// VACUUM INTO 在 WAL 模式下生成一致快照且不阻塞读写；路径需转义后嵌入 SQL。
	escaped := strings.ReplaceAll(snap, "'", "''")
	if _, err := db.ExecContext(ctx, fmt.Sprintf("VACUUM INTO '%s'", escaped)); err != nil {
		_ = os.RemoveAll(dir) // 坏档不残留，半成品备份比没有备份更危险。
		return Manifest{}, fmt.Errorf("store: 快照失败: %w", err)
	}
	man, err := verifySnapshot(ctx, snap, now)
	if err != nil {
		_ = os.RemoveAll(dir)
		return Manifest{}, err // 已含 %w，不再包裹。
	}
	if err := writeManifest(dir, man); err != nil {
		_ = os.RemoveAll(dir)
		return Manifest{}, err
	}
	return man, nil
}

// verifySnapshot 检查数据库完整性、行数、迁移版本和文件哈希。
func verifySnapshot(ctx context.Context, snap string, now time.Time) (Manifest, error) {
	db, err := openPlain(ctx, snap)
	if err != nil {
		return Manifest{}, fmt.Errorf("store: 打开快照校验: %w", err)
	}
	defer func() { _ = db.Close() }()
	var integrity string
	if err := db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil {
		return Manifest{}, fmt.Errorf("store: 完整性自检执行失败: %w", err)
	}
	if integrity != "ok" {
		return Manifest{}, fmt.Errorf("store: 快照完整性自检未过: %s", integrity)
	}
	man := Manifest{
		MuniuVersion: version.Version,
		TakenAtUTC:   now.UTC().Format(time.RFC3339),
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM api_keys").Scan(&man.APIKeys); err != nil {
		return Manifest{}, fmt.Errorf("store: 统计 api_keys: %w", err)
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM usage_logs").Scan(&man.UsageLogs); err != nil {
		return Manifest{}, fmt.Errorf("store: 统计 usage_logs: %w", err)
	}
	if err := db.QueryRowContext(ctx,
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

// Restore 将 dir 中的备份恢复到 destPath；覆盖已有数据库需设置 force。
func Restore(ctx context.Context, dir, destPath string, force bool) error {
	man, err := readManifest(dir)
	if err != nil {
		return err
	}
	snap := filepath.Join(dir, backupDBName)
	if _, err := os.Stat(snap); err != nil {
		return fmt.Errorf("store: 备份缺快照文件: %w", err)
	}
	if err := verifyBackup(ctx, snap, man); err != nil {
		return err
	}
	if err := checkTarget(destPath, force); err != nil {
		return err
	}
	return swapInSnapshot(ctx, snap, destPath)
}

// verifyBackup 恢复前校验备份：文件哈希、整库自检与行数都须与清单一致。
func verifyBackup(ctx context.Context, snap string, man Manifest) error {
	// 先检查快照文件哈希，避免触碰目标前接受损坏备份。
	sum, err := fileSHA256(snap)
	if err != nil {
		return err
	}
	if sum != man.SHA256 {
		return fmt.Errorf("store: 快照 sha256 与 manifest 不符(备份损坏或被改动)")
	}
	// 确认数据库完整性和行数与清单一致。
	if _, err := verifySnapshot(ctx, snap, time.Now().UTC()); err != nil {
		return err
	}
	got, err := snapshotCounts(ctx, snap)
	if err != nil {
		return err
	}
	if got.APIKeys != man.APIKeys || got.UsageLogs != man.UsageLogs {
		return fmt.Errorf(
			"store: 备份行数与 manifest 不符(keys %d/%d, usage %d/%d)",
			got.APIKeys, man.APIKeys, got.UsageLogs, man.UsageLogs,
		)
	}
	return nil
}

// checkTarget 目标被数据库连接占用时拒绝恢复；未置 force 时拒绝覆盖。
func checkTarget(destPath string, force bool) error {
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
	return nil
}

// swapInSnapshot 落位快照并复校验；任一步失败都把原库(及其侧车)换回原位。
func swapInSnapshot(ctx context.Context, snap, destPath string) error {
	// 主库及其 WAL/SHM 文件必须整体替换，避免旧 WAL 帧写入恢复后的数据库。
	sidecars := []string{"", "-wal", "-shm"}
	tmp := destPath + ".restore-tmp"
	if err := copyFile(snap, tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("store: 写入恢复临时文件: %w", err)
	}
	preRestore := destPath + ".pre-restore"
	hadDest, moved, err := moveAside(destPath, preRestore, sidecars)
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, destPath); err != nil {
		if hadDest {
			restoreAside(destPath, preRestore, moved)
		}
		_ = os.Remove(tmp)
		return fmt.Errorf("store: 落位恢复文件: %w", err)
	}
	// 再次验证恢复结果；失败时恢复原目标文件。
	if _, err := verifySnapshot(ctx, destPath, time.Now().UTC()); err != nil {
		for _, s := range sidecars {
			_ = os.Remove(destPath + s)
		}
		if hadDest {
			restoreAside(destPath, preRestore, moved)
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

// moveAside 把目标库及其已存在的侧车文件改名到 preRestore 前缀下，返回已移动后缀供回滚。
// 中途失败时已移动的文件当场换回，不留半态现场。
func moveAside(destPath, preRestore string, sidecars []string) (bool, []string, error) {
	switch _, err := os.Stat(destPath); {
	case errors.Is(err, fs.ErrNotExist):
		return false, nil, nil
	case err != nil:
		return false, nil, fmt.Errorf("store: 检查恢复目标: %w", err)
	}
	var moved []string
	for _, s := range sidecars {
		if _, err := os.Stat(destPath + s); errors.Is(err, fs.ErrNotExist) {
			continue // 侧车文件可能本就不存在，跳过。
		} else if err != nil {
			restoreAside(destPath, preRestore, moved)
			return false, nil, fmt.Errorf("store: 检查侧车文件: %w", err)
		}
		if err := os.Rename(destPath+s, preRestore+s); err != nil {
			restoreAside(destPath, preRestore, moved)
			return false, nil, fmt.Errorf("store: 保留现场失败: %w", err)
		}
		moved = append(moved, s)
	}
	return true, moved, nil
}

// restoreAside 把 moveAside 换走的文件换回原位；单文件失败不阻断其余回滚。
func restoreAside(destPath, preRestore string, moved []string) {
	for _, m := range moved {
		_ = os.Rename(preRestore+m, destPath+m)
	}
}

// probeLocked 尝试获取数据库文件的写锁；失败表示目标可能仍被 SQLite 连接占用。
func probeLocked(path string) error {
	for _, p := range []string{path, path + "-shm"} {
		f, err := os.OpenFile(p, os.O_RDWR, 0) //nolint:gosec // 路径为运维指定的库文件及其 -shm
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

// snapshotCounts 复算快照行数，与 readManifest 的值比对。
func snapshotCounts(ctx context.Context, snap string) (counts, error) {
	db, err := openPlain(ctx, snap)
	if err != nil {
		return counts{}, fmt.Errorf("store: 打开快照统计: %w", err)
	}
	defer func() { _ = db.Close() }()
	var c counts
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM api_keys").Scan(&c.APIKeys); err != nil {
		return counts{}, fmt.Errorf("store: 统计 api_keys: %w", err)
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM usage_logs").Scan(&c.UsageLogs); err != nil {
		return counts{}, fmt.Errorf("store: 统计 usage_logs: %w", err)
	}
	return c, nil
}

func readManifest(dir string) (Manifest, error) {
	raw, err := os.ReadFile(filepath.Join(dir, manifestName)) //nolint:gosec // dir 为运维指定的备份目录
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

// openPlain 以最裸形态打开快照，只作只读校验，不走 Open 的 pragma 与迁移。
func openPlain(ctx context.Context, path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path) //nolint:gosec // 备份目录内文件路径由本包快照产物派生
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
	in, err := os.Open(src) //nolint:gosec // 备份目录内文件路径由本包快照产物派生
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm) //nolint:gosec // 同上,目标在备份/恢复目录内
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
