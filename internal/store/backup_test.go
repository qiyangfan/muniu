package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite" // 只读打开用(与 Open 同一驱动)
)

// seedRows 造最小可核账状态:1 租户 + 2 key + 3 条 usage。
func seedRows(t *testing.T, db *sql.DB) {
	t.Helper()
	mustExec(t, db,
		"INSERT INTO tenants (id, name, status, created_at)"+
		" VALUES ('t1', 'test-tenant', 'active', '2026-09-30T00:00:00Z')")
	mustExec(t, db,
		"INSERT INTO api_keys (id, tenant_id, name, key_hash, prefix, created_at)"+
			" VALUES ('k1', 't1', 'kongming', 'hash1', 'mk-AAAAAAAA', '2026-09-30T00:00:00Z')")
	mustExec(t, db,
		"INSERT INTO api_keys (id, tenant_id, name, key_hash, prefix, created_at)"+
			" VALUES ('k2', 't1', 'claude', 'hash2', 'mk-BBBBBBBB', '2026-09-30T00:00:01Z')")
	for i, st := range []string{"ok", "ok", "error"} {
		mustExec(t, db, fmt.Sprintf(
			"INSERT INTO usage_logs (id, request_id, tenant_id, api_key_id, provider,"+
				" model_alias, upstream_model, stream, status, latency_total_ms, created_at)"+
				" VALUES ('u%d', 'r%d', 't1', 'k1', 'qwen', 'qwen-plus', 'qwen-plus', 1, '%s',"+
				" 12, '2026-09-30T00:00:0%dZ')", i+1, i+1, st, i))
	}
}

func mustExec(t *testing.T, db *sql.DB, q string) {
	t.Helper()
	if _, err := db.Exec(q); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}

func countOf(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM %s", table)).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func openDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	return db
}

// TestSnapshotRestoreRoundtrip:备份→恢复 roundtrip,三处行数与内容一致。
func TestSnapshotRestoreRoundtrip(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.db")
	db := openDB(t, src)
	seedRows(t, db)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db2, err := Open(src)
	if err != nil {
		t.Fatal(err)
	}
	backupDir := filepath.Join(dir, "bak")
	man, err := Snapshot(db2, backupDir, time.Now())
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	_ = db2.Close()
	if man.APIKeys != 2 || man.UsageLogs != 3 {
		t.Fatalf("manifest 行数不符: %+v", man)
	}
	if man.MuniuVersion == "" || man.SHA256 == "" {
		t.Fatalf("manifest 缺版本/哈希: %+v", man)
	}
	if man.MigrationsVersion <= 0 {
		t.Fatalf("migrations 版本异常: %+v", man)
	}

	dest := filepath.Join(dir, "restored.db")
	if err := Restore(backupDir, dest, false); err != nil {
		t.Fatalf("restore: %v", err)
	}
	rdb := openDB(t, dest)
	defer func() { _ = rdb.Close() }()
	if got := countOf(t, rdb, "api_keys"); got != 2 {
		t.Fatalf("api_keys = %d, want 2", got)
	}
	if got := countOf(t, rdb, "usage_logs"); got != 3 {
		t.Fatalf("usage_logs = %d, want 3", got)
	}
	// 内容级一致(key 前缀逐字)
	var prefix string
	if err := rdb.QueryRow("SELECT prefix FROM api_keys WHERE id='k2'").Scan(&prefix); err != nil {
		t.Fatal(err)
	}
	if prefix != "mk-BBBBBBBB" {
		t.Fatalf("prefix = %q", prefix)
	}
}

// TestSnapshotIncludesUnmergedWAL:写入后不 checkpoint 直接备份,
// VACUUM INTO 一致快照须含已提交 WAL 帧(不丢不旧)。
func TestSnapshotIncludesUnmergedWAL(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "wal.db")
	db := openDB(t, src)
	seedRows(t, db)
	// 关键机制:连接**未关闭**(SQLite 末连接关闭才 checkpoint 合并),
	// 数据仍以 WAL 帧存在;VACUUM INTO 的一致快照须含已提交帧。
	// (早先版本在同池持读事务,撞 SetMaxOpenConns(1) 自锁——真实 CLI
	// 是独立进程开自身连接,无此竞争;勿把测试写成自锁形状。)
	backupDir := filepath.Join(dir, "bak")
	if _, err := Snapshot(db, backupDir, time.Now()); err != nil {
		t.Fatalf("snapshot(连接未关闭): %v", err)
	}
	_ = db.Close()

	dest := filepath.Join(dir, "restored.db")
	if err := Restore(backupDir, dest, false); err != nil {
		t.Fatalf("restore: %v", err)
	}
	rdb := openDB(t, dest)
	defer func() { _ = rdb.Close() }()
	if got := countOf(t, rdb, "usage_logs"); got != 3 {
		t.Fatalf("usage_logs = %d, want 3(未合并 WAL 丢失?)", got)
	}
}

// TestSnapshotRefusesExistingDir:备份目录已存在即拒(防混入既有备份)。
func TestSnapshotRefusesExistingDir(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.db")
	db := openDB(t, src)
	defer func() { _ = db.Close() }()
	seedRows(t, db)
	existing := filepath.Join(dir, "existing")
	if err := os.MkdirAll(existing, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Snapshot(db, existing, time.Now()); err == nil {
		t.Fatal("期望拒绝已存在目录")
	}
}

// TestSnapshotDeletesArtifactOnFailure:快照失败(VACUUM INTO 报错)时
// 删档不残留——半成品备份比没有备份更危险。
func TestSnapshotDeletesArtifactOnFailure(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "ro.db")
	db := openDB(t, src)
	seedRows(t, db)
	_ = db.Close()
	// query_only 重开:VACUUM INTO 必失败(attempt to write a readonly database)
	// (modernc 不认裸 mode=ro 查询参,query_only pragma 是本仓 Open 同款注入形态)
	ro, err := sql.Open("sqlite", src+"?_pragma=query_only(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ro.Close() }()
	ro.SetMaxOpenConns(1)
	backupDir := filepath.Join(dir, "bak")
	if _, err := Snapshot(ro, backupDir, time.Now()); err == nil {
		t.Fatal("期望快照失败")
	}
	if _, err := os.Stat(backupDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("失败后备份目录应被删除,实际: %v", err)
	}
}

// TestRestoreRefusesExistingDestWithoutForce:目标已存在未传 force 即拒,
// 且目标内容零改动。
func TestRestoreRefusesExistingDestWithoutForce(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.db")
	db := openDB(t, src)
	seedRows(t, db) // 备份源=3 条 usage(manifest 记 3)
	backupDir := filepath.Join(dir, "bak")
	man, err := Snapshot(db, backupDir, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	dest := filepath.Join(dir, "dest.db")
	// 目标故意造成不同内容(4 条 usage)
	ddb := openDB(t, dest)
	seedRows(t, ddb)
	mustExec(t, ddb,
		"INSERT INTO usage_logs (id, request_id, tenant_id, api_key_id, provider,"+
			" model_alias, upstream_model, stream, status, latency_total_ms, created_at)"+
			" VALUES ('u4', 'r4', 't1', 'k1', 'qwen', 'qwen-plus', 'qwen-plus', 1, 'ok',"+
			" 4, '2026-09-30T02:00:00Z')")
	_ = ddb.Close()

	if err := Restore(backupDir, dest, false); err == nil {
		t.Fatal("期望拒绝覆盖")
	}
	// 目标零改动:仍 4 条(manifest 记的是 3 条备份)
	rdb := openDB(t, dest)
	defer func() { _ = rdb.Close() }()
	if got := countOf(t, rdb, "usage_logs"); got != 4 {
		t.Fatalf("usage_logs = %d, want 4(目标被改动?)", got)
	}
	if man.UsageLogs != 3 {
		t.Fatalf("manifest usage = %d, want 3", man.UsageLogs)
	}
	// force 后成功覆盖
	if err := Restore(backupDir, dest, true); err != nil {
		t.Fatalf("force restore: %v", err)
	}
	rdb2 := openDB(t, dest)
	defer func() { _ = rdb2.Close() }()
	if got := countOf(t, rdb2, "usage_logs"); got != 3 {
		t.Fatalf("force 后 usage_logs = %d, want 3", got)
	}
}

// TestRestoreRejectsManifestMismatch:manifest 行数与快照实际不符 → 拒收,
// 目标保持原样(spec「恢复后校验不符/坏档拒收」)。
func TestRestoreRejectsManifestMismatch(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.db")
	db := openDB(t, src)
	seedRows(t, db)
	backupDir := filepath.Join(dir, "bak")
	if _, err := Snapshot(db, backupDir, time.Now()); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	// 篡改 manifest 的 usage_logs 行数(模拟坏档/对不齐)
	manPath := filepath.Join(backupDir, manifestName)
	raw, err := os.ReadFile(manPath)
	if err != nil {
		t.Fatal(err)
	}
	var man Manifest
	if err := json.Unmarshal(raw, &man); err != nil {
		t.Fatal(err)
	}
	man.UsageLogs = 99
	tampered, err := json.MarshalIndent(man, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manPath, tampered, 0o600); err != nil {
		t.Fatal(err)
	}

	dest := filepath.Join(dir, "dest.db")
	if err := Restore(backupDir, dest, false); err == nil {
		t.Fatal("期望拒收行数不符的备份")
	}
	if _, err := os.Stat(dest); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("拒收后目标不应被创建: %v", err)
	}
}

// TestRestoreRejectsTamperedSnapshot:快照文件被改动(sha256 不符 manifest)→ 拒。
func TestRestoreRejectsTamperedSnapshot(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.db")
	db := openDB(t, src)
	seedRows(t, db)
	backupDir := filepath.Join(dir, "bak")
	if _, err := Snapshot(db, backupDir, time.Now()); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	// 追加脏字节到快照文件(SQLite 容忍尾部垃圾,integrity 可能仍过,
	// 但 sha256 必须抓到)
	f, err := os.OpenFile(filepath.Join(backupDir, backupDBName), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("garbage"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	dest := filepath.Join(dir, "dest.db")
	if err := Restore(backupDir, dest, false); err == nil {
		t.Fatal("期望 sha256 不符拒收")
	}
}

// TestRestoreRejectsMissingManifestDir:非 muniu 备份目录(缺 manifest)→ 拒。
func TestRestoreRejectsMissingManifestDir(t *testing.T) {
	dir := t.TempDir()
	if err := Restore(dir, filepath.Join(dir, "x.db"), false); err == nil {
		t.Fatal("期望拒绝无 manifest 的目录")
	}
}

// TestRestoreRefusesLockedTarget:目标被另一进程的 SQLite 连接持锁 →
// 拒恢复(实例在运行;POSIX 锁跨进程实测可见,同进程不冲突故用子进程)。
func TestRestoreRefusesLockedTarget(t *testing.T) {
	if flag.Lookup("test.v") == nil {
		t.Fatal("test 标志缺失?")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "src.db")
	db := openDB(t, src)
	seedRows(t, db)
	backupDir := filepath.Join(dir, "bak")
	if _, err := Snapshot(db, backupDir, time.Now()); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	dest := filepath.Join(dir, "dest.db")
	hdb := openDB(t, dest)
	seedRows(t, hdb)

	// 子进程持有 dest 的 SQLite 连接(空闲连接亦持 POSIX 锁,实验实证)
	cmd := exec.Command(os.Args[0], "-test.run=TestLockHolderHelper", "-test.v=false") //nolint:gosec // 测试二进制自身
	cmd.Env = append(os.Environ(), "MUNIU_LOCK_DB="+dest)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()
	ready := make(chan struct{})
	go func() {
		buf := make([]byte, 64)
		for {
			n, err := stdout.Read(buf)
			if err != nil || strings.Contains(string(buf[:n]), "ready") {
				close(ready)
				return
			}
		}
	}()
	select {
	case <-ready:
	case <-time.After(10 * time.Second):
		t.Fatal("等待锁持有子进程超时")
	}

	err = Restore(backupDir, dest, true) // force 也不行:占用闸先于覆盖闸
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("期望 ErrLocked,实际: %v", err)
	}
	// 目标零改动
	if got := countOf(t, hdb, "usage_logs"); got != 3 {
		t.Fatalf("usage_logs = %d, want 3", got)
	}
	_ = hdb.Close()
}

// TestLockHolderHelper 不作为用例运行(由 TestRestoreRefusesLockedTarget
// 以子进程拉起):打开指定库并保持连接,打印 ready。
func TestLockHolderHelper(t *testing.T) {
	path := os.Getenv("MUNIU_LOCK_DB")
	if path == "" {
		t.Skip("非锁持有子进程,跳过")
	}
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master").Scan(&n); err != nil {
		t.Fatal(err)
	}
	fmt.Println("ready")
	time.Sleep(30 * time.Second)
}

// TestProbeLockedFreeWhenNoConnection:无连接时探测通过(回归:误报会让
// 所有恢复被拒)。
func TestProbeLockedFreeWhenNoConnection(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "free.db")
	db := openDB(t, p)
	seedRows(t, db)
	_ = db.Close()
	if err := probeLocked(p); err != nil {
		t.Fatalf("无连接时应可探测通过: %v", err)
	}
	// 不存在的文件同样通过(新目标无占用概念)
	if err := probeLocked(filepath.Join(dir, "nope.db")); err != nil {
		t.Fatalf("不存在目标应通过: %v", err)
	}
}

// TestRestoreReplacesStaleWalSidecars:目标库带崩溃残留 -wal/-shm(unclean
// shutdown)时,恢复须将旧 WAL 帧整体换走——单独替换主库文件会把陈旧 WAL
// 留给新库,下次打开旧帧被恢复叠到新库上(静默数据混杂)。
// 复现构造:另一库写入不合并 WAL → 整对拷到目标路径(不经 clean close)。
func TestRestoreReplacesStaleWalSidecars(t *testing.T) {
	dir := t.TempDir()
	// 备份源:3 条 usage
	src := filepath.Join(dir, "src.db")
	sdb := openDB(t, src)
	seedRows(t, sdb)
	backupDir := filepath.Join(dir, "bak")
	if _, err := Snapshot(sdb, backupDir, time.Now()); err != nil {
		t.Fatal(err)
	}
	_ = sdb.Close()

	// 旧目标:4 条 usage(第 4 条只存在于 WAL 帧,未合并主库)
	stale := filepath.Join(dir, "stale-src.db")
	tdb := openDB(t, stale)
	seedRows(t, tdb)
	mustExec(t, tdb,
		"INSERT INTO usage_logs (id, request_id, tenant_id, api_key_id, provider,"+
			" model_alias, upstream_model, stream, status, latency_total_ms, created_at)"+
			" VALUES ('uX', 'rX', 't1', 'k1', 'qwen', 'qwen-plus', 'qwen-plus', 1, 'ok',"+
			" 7, '2026-09-30T03:00:00Z')")
	dest := filepath.Join(dir, "dest.db")
	for _, suffix := range []string{"", "-wal", "-shm"} {
		raw, err := os.ReadFile(stale + suffix)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			t.Fatal(err)
		}
		if err := os.WriteFile(dest+suffix, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	_ = tdb.Close() // 关旧库(其自身 sidecar 清掉,目标侧副本保留)
	// 注意:勿在恢复前打开 dest——那会WAL 恢复+checkpoint+clean close 删掉
	// sidecar,把待测条件亲手销毁(首版用例即此假绿)。

	if err := Restore(backupDir, dest, true); err != nil {
		t.Fatalf("restore: %v", err)
	}
	rdb := openDB(t, dest)
	defer func() { _ = rdb.Close() }()
	if got := countOf(t, rdb, "usage_logs"); got != 3 {
		t.Fatalf("恢复后 usage = %d, want 3(陈旧 WAL 帧被叠加到新库!)", got)
	}
	var staleRow int
	if err := rdb.QueryRow(
		"SELECT COUNT(*) FROM usage_logs WHERE id = 'uX'").Scan(&staleRow); err != nil {
		t.Fatal(err)
	}
	if staleRow != 0 {
		t.Fatal("陈旧 WAL 中的 uX 行复活=数据混杂缺陷")
	}
	var integrity string
	if err := rdb.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil {
		t.Fatal(err)
	}
	if integrity != "ok" {
		t.Fatalf("恢复后 integrity = %s", integrity)
	}
}
