// break-glass 测试：独立凭证/时间窗/确认/审计/行数上限；审计失败整体回滚。
package breakglass

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/solvela/muniu/internal/store"
)

const testToken = "break-glass-token-0123456789"

func setup(t *testing.T) (dbPath, credPath string, now time.Time) {
	t.Helper()
	dir := t.TempDir()
	dbPath = filepath.Join(dir, "muniu.db")
	db, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now = time.Now().UTC()
	sum := sha256.Sum256([]byte(testToken))
	cred, _ := json.Marshal(Credential{
		TokenSHA256: hex.EncodeToString(sum[:]),
		NotBefore:   now.Add(-time.Hour).Format(time.RFC3339),
		NotAfter:    now.Add(time.Hour).Format(time.RFC3339),
	})
	credPath = filepath.Join(dir, "credential.json")
	if err := os.WriteFile(credPath, cred, 0o600); err != nil {
		t.Fatal(err)
	}
	return dbPath, credPath, now
}

func baseOpts(dbPath, credPath string, now time.Time) Options {
	return Options{
		DBPath: dbPath, CredentialPath: credPath, Token: testToken,
		Op: "purge-expired-semantic-cache", Reason: "应急清理",
		Params: map[string]any{}, MaxRows: 100, Now: now,
		Confirm: func(string, string) (bool, error) { return true, nil },
	}
}

func insertCache(t *testing.T, dbPath, id, expires string) {
	t.Helper()
	db, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(context.Background(), `INSERT INTO semantic_cache
		(id, tenant_id, fingerprint, model_alias, embedding, response, hits, created_at, expires_at)
		VALUES (?,?,?,?,?,?,0,?,?)`,
		id, "01900000-0000-7000-8000-000000000001", id, "m", []byte{0}, []byte(`{}`),
		time.Now().UTC().Format(time.RFC3339), expires); err != nil {
		t.Fatal(err)
	}
}

func count(t *testing.T, dbPath, query string, args ...any) int64 {
	t.Helper()
	db, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var n int64
	if err := db.QueryRowContext(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestBreakGlass_凭证与时间窗先于存储(t *testing.T) {
	dbPath, credPath, now := setup(t)
	opts := baseOpts(dbPath, credPath, now)
	opts.Token = ""
	if _, err := Run(context.Background(), opts); err == nil || !strings.Contains(err.Error(), "token") {
		t.Fatalf("缺 token 必须拒绝: %v", err)
	}
	opts = baseOpts(dbPath, credPath, now)
	opts.Token = "wrong-token-0123456789abcdef"
	if _, err := Run(context.Background(), opts); err == nil {
		t.Fatal("错误 token 必须拒绝")
	}
	// 过期窗口
	expired := filepath.Join(t.TempDir(), "dead.json")
	sum := sha256.Sum256([]byte(testToken))
	raw, _ := json.Marshal(Credential{
		TokenSHA256: hex.EncodeToString(sum[:]),
		NotBefore:   now.Add(-2 * time.Hour).Format(time.RFC3339),
		NotAfter:    now.Add(-time.Hour).Format(time.RFC3339),
	})
	if err := os.WriteFile(expired, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	opts = baseOpts(dbPath, expired, now)
	if _, err := Run(context.Background(), opts); err == nil || !strings.Contains(err.Error(), "时间窗") {
		t.Fatalf("过期窗口必须拒绝: %v", err)
	}
	// 权限过宽
	loose := filepath.Join(t.TempDir(), "loose.json")
	if err := os.WriteFile(loose, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	opts = baseOpts(dbPath, loose, now)
	if _, err := Run(context.Background(), opts); err == nil || !strings.Contains(err.Error(), "0600") {
		t.Fatalf("凭证权限过宽必须拒绝: %v", err)
	}
}

func TestBreakGlass_无凭证不创建数据库(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "never.db")
	credPath := filepath.Join(dir, "cred.json")
	if err := os.WriteFile(credPath, []byte(`{"token_sha256":"00","not_before":"2020-01-01T00:00:00Z","not_after":"2030-01-01T00:00:00Z"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	opts := baseOpts(dbPath, credPath, time.Now())
	opts.Token = ""
	if _, err := Run(context.Background(), opts); err == nil {
		t.Fatal("无凭证必须拒绝")
	}
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Fatal("凭证校验失败前不得连接/创建存储")
	}
}

func TestBreakGlass_存储不存在不新建(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "missing.db")
	credPath := filepath.Join(dir, "cred.json")
	now := time.Now().UTC()
	sum := sha256.Sum256([]byte(testToken))
	raw, _ := json.Marshal(Credential{
		TokenSHA256: hex.EncodeToString(sum[:]),
		NotBefore:   now.Add(-time.Hour).Format(time.RFC3339),
		NotAfter:    now.Add(time.Hour).Format(time.RFC3339),
	})
	if err := os.WriteFile(credPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	opts := baseOpts(dbPath, credPath, now)
	if _, err := Run(context.Background(), opts); err == nil || !strings.Contains(err.Error(), "不存在") {
		t.Fatalf("存储缺失必须拒绝: %v", err)
	}
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Fatal("break-glass 不得新建存储")
	}
}

func TestBreakGlass_清理过期缓存与审计(t *testing.T) {
	dbPath, credPath, now := setup(t)
	insertCache(t, dbPath, "expired-1", now.Add(-time.Hour).Format(time.RFC3339))
	insertCache(t, dbPath, "active-1", now.Add(time.Hour).Format(time.RFC3339))
	report, err := Run(context.Background(), baseOpts(dbPath, credPath, now))
	if err != nil {
		t.Fatal(err)
	}
	if report.Rows != 1 || report.AuditID == "" {
		t.Fatalf("应删除 1 行并留下审计: %+v", report)
	}
	if n := count(t, dbPath, `SELECT COUNT(*) FROM semantic_cache`); n != 1 {
		t.Fatalf("只应保留未过期行: %d", n)
	}
	if n := count(t, dbPath, `SELECT COUNT(*) FROM admin_audit_log WHERE action='break_glass.purge-expired-semantic-cache'`); n != 1 {
		t.Fatalf("审计应留痕: %d", n)
	}
	var detail string
	db, _ := store.Open(context.Background(), dbPath)
	defer func() { _ = db.Close() }()
	if err := db.QueryRowContext(context.Background(),
		`SELECT detail FROM admin_audit_log WHERE id=?`, report.AuditID).Scan(&detail); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(detail, "应急清理") || !strings.Contains(detail, `"rows":1`) {
		t.Fatalf("审计 detail 须含原因与行数: %s", detail)
	}
}

func TestBreakGlass_行数上限(t *testing.T) {
	dbPath, credPath, now := setup(t)
	for _, id := range []string{"e1", "e2", "e3"} {
		insertCache(t, dbPath, id, now.Add(-time.Hour).Format(time.RFC3339))
	}
	opts := baseOpts(dbPath, credPath, now)
	opts.MaxRows = 2
	report, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if report.Rows != 2 {
		t.Fatalf("应只删 2 行: %d", report.Rows)
	}
	if n := count(t, dbPath, `SELECT COUNT(*) FROM semantic_cache`); n != 1 {
		t.Fatalf("应剩 1 行: %d", n)
	}
	opts.MaxRows = 1001
	if _, err := Run(context.Background(), opts); err == nil {
		t.Fatal("超过硬顶必须拒绝")
	}
}

func TestBreakGlass_取消不执行(t *testing.T) {
	dbPath, credPath, now := setup(t)
	insertCache(t, dbPath, "e1", now.Add(-time.Hour).Format(time.RFC3339))
	opts := baseOpts(dbPath, credPath, now)
	opts.Confirm = func(string, string) (bool, error) { return false, nil }
	if _, err := Run(context.Background(), opts); err == nil || !strings.Contains(err.Error(), "取消") {
		t.Fatalf("取消必须终止: %v", err)
	}
	if n := count(t, dbPath, `SELECT COUNT(*) FROM semantic_cache`); n != 1 {
		t.Fatalf("取消不得修改数据: %d", n)
	}
}

func TestBreakGlass_确认摘要携带参数(t *testing.T) {
	dbPath, credPath, now := setup(t)
	opts := baseOpts(dbPath, credPath, now)
	opts.Op = "drop-buffered-usage"
	opts.Params = map[string]any{"seq": float64(7)}
	var summary string
	opts.Confirm = func(_, s string) (bool, error) { summary = s; return false, nil }
	if _, err := Run(context.Background(), opts); err == nil {
		t.Fatal("取消必须终止")
	}
	if !strings.Contains(summary, "seq") || !strings.Contains(summary, "7") {
		t.Fatalf("确认摘要必须展示参数: %q", summary)
	}
}

func TestBreakGlass_审计失败整体回滚(t *testing.T) {
	dbPath, credPath, now := setup(t)
	insertCache(t, dbPath, "e1", now.Add(-time.Hour).Format(time.RFC3339))
	db, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `DROP TABLE admin_audit_log`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if _, err := Run(context.Background(), baseOpts(dbPath, credPath, now)); err == nil {
		t.Fatal("审计写失败必须拒绝并回滚")
	}
	if n := count(t, dbPath, `SELECT COUNT(*) FROM semantic_cache`); n != 1 {
		t.Fatalf("审计失败时修复必须回滚: %d", n)
	}
}

func TestBreakGlass_操作白名单与参数(t *testing.T) {
	dbPath, credPath, now := setup(t)
	opts := baseOpts(dbPath, credPath, now)
	opts.Op = "drop-everything"
	if _, err := Run(context.Background(), opts); err == nil {
		t.Fatal("未注册操作必须拒绝")
	}
	opts = baseOpts(dbPath, credPath, now)
	opts.Op = "drop-buffered-usage"
	if _, err := Run(context.Background(), opts); err == nil {
		t.Fatal("缺 seq 必须拒绝")
	}
	// seq 是行身份：小数必须拒绝，禁止截断后误删相邻行
	opts = baseOpts(dbPath, credPath, now)
	opts.Op = "drop-buffered-usage"
	opts.Params = map[string]any{"seq": 1.9}
	if _, err := Run(context.Background(), opts); err == nil || !strings.Contains(err.Error(), "正整数") {
		t.Fatalf("小数 seq 必须拒绝: %v", err)
	}
	ops := Operations()
	if len(ops) != 4 {
		t.Fatalf("文档化操作清单应为 4 条: %d", len(ops))
	}
}

func TestBreakGlass_单行操作忽略缺省上限(t *testing.T) {
	dbPath, credPath, now := setup(t)
	db, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `INSERT INTO metering_buffer(row_id,payload,created_at)
		VALUES('r1','{}','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	var seq int64
	if err := func() error {
		d, err := store.Open(context.Background(), dbPath)
		if err != nil {
			return err
		}
		defer func() { _ = d.Close() }()
		return d.QueryRowContext(context.Background(), `SELECT seq FROM metering_buffer LIMIT 1`).Scan(&seq)
	}(); err != nil {
		t.Fatal(err)
	}
	opts := baseOpts(dbPath, credPath, now)
	opts.Op = "drop-buffered-usage"
	opts.Params = map[string]any{"seq": float64(seq)}
	report, err := Run(context.Background(), opts)
	if err != nil || report.Rows != 1 {
		t.Fatalf("单行操作应成功: %+v %v", report, err)
	}
}
