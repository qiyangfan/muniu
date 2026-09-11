// store 契约测试:迁移幂等、default 租户种子、repo 三方法(查 key/批量落库幂等/日成本聚合)。
package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestMigrate_幂等_重复打开不报错(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "muniu.db")

	db1, err := Open(p)
	if err != nil {
		t.Fatalf("首次打开失败: %v", err)
	}
	_ = db1.Close()

	db2, err := Open(p) // 第二次打开:已应用迁移必须全部跳过
	if err != nil {
		t.Fatalf("二次打开(迁移应幂等)失败: %v", err)
	}
	defer func() { _ = db2.Close() }()

	var n int
	if err := db2.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n < 1 {
		t.Fatalf("迁移账不应为空,实际 %d", n)
	}
	var tables int
	if err := db2.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name IN ('tenants','api_keys','usage_logs')`).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if tables != 3 {
		t.Fatalf("契约三表应齐备,实际 %d", tables)
	}
}

func TestMigrate_default租户种子存在(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "muniu.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	var name, status string
	err = db.QueryRow(`SELECT name, status FROM tenants WHERE id = '01900000-0000-7000-8000-000000000001'`).Scan(&name, &status)
	if err != nil {
		t.Fatalf("default 租户种子缺失: %v", err)
	}
	if name != "default" || status != "active" {
		t.Fatalf("种子内容不符: %s %s", name, status)
	}
}

func TestGetAPIKeyByHash_查得与未查得(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "muniu.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	now := time.Now().UTC().Format(time.RFC3339)
	_, err = db.Exec(`INSERT INTO api_keys (id, tenant_id, name, key_hash, prefix, status, rpm_limit, created_at, revoked_at)
		VALUES (?,?,?,?,?,?,?,?,NULL)`,
		uuid.Must(uuid.NewV7()).String(), "01900000-0000-7000-8000-000000000001",
		"测试key", "deadbeef", "mk-abcdef12", "active", 120, now)
	if err != nil {
		t.Fatalf("插入测试 key: %v", err)
	}

	got, err := GetAPIKeyByHash(db, "deadbeef")
	if err != nil {
		t.Fatalf("按 hash 查询失败: %v", err)
	}
	if got.Name != "测试key" || got.RPMLimit != 120 || got.Status != "active" || got.TenantID != "01900000-0000-7000-8000-000000000001" {
		t.Fatalf("字段回读不符: %+v", got)
	}

	if _, err := GetAPIKeyByHash(db, "no-such-hash"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("未命中应返回 ErrNotFound,实际 %v", err)
	}
}

func TestInsertUsageBatch_批量写入与幂等(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "muniu.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	first := int64(12)
	row := UsageRow{
		ID: "01900000-0000-7000-8000-0000000000aa", RequestID: "req-1",
		TenantID: "01900000-0000-7000-8000-000000000001", APIKeyID: "key-1",
		Provider: "deepseek", ModelAlias: "alias-a", UpstreamModel: "up-a",
		Stream: true, Status: "ok", PromptTokens: 10, CompletionTokens: 20,
		Estimated: false, CostMicro: 8800, Currency: "CNY",
		LatencyFirstTokenMs: &first, LatencyTotalMs: 500,
		CreatedAt: time.Now().UTC(),
	}
	if err := InsertUsageBatch(db, []UsageRow{row, row}); err != nil {
		t.Fatalf("批量写入失败: %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM usage_logs`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("重复 id 应被 OR IGNORE 去重,实际 %d 行", n)
	}

	var stream int64
	var errClass any
	err = db.QueryRow(`SELECT stream, error_class FROM usage_logs WHERE id = ?`, row.ID).Scan(&stream, &errClass)
	if err != nil {
		t.Fatal(err)
	}
	if stream != 1 || errClass != nil {
		t.Fatalf("布尔/可空列写入不符: stream=%d err=%v", stream, errClass)
	}
}

func TestSumCostToday_过滤estimated与当日(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "muniu.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	now := time.Now().UTC()
	mk := func(id, status string, estimated bool, cost int64, at time.Time) UsageRow {
		return UsageRow{ID: id, RequestID: id, TenantID: "t1", APIKeyID: "k1",
			Provider: "p", ModelAlias: "m", UpstreamModel: "u",
			Status: status, Estimated: estimated, CostMicro: cost,
			LatencyTotalMs: 1, CreatedAt: at}
	}
	rows := []UsageRow{
		mk("a", "ok", false, 100, now),
		mk("b", "ok", false, 200, now),
		mk("c", "error", false, 400, now),                  // 失败请求也聚合(error_class 不影响成本口径)
		mk("d", "ok", true, 800, now),                      // estimated 行必须被剔除
		mk("e", "ok", false, 1600, now.Add(-25*time.Hour)), // 昨日不计
	}
	if err := InsertUsageBatch(db, rows); err != nil {
		t.Fatal(err)
	}

	total, err := SumCostToday(db, "", "k1", now.Format("2006-01-02T00:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if total != 700 {
		t.Fatalf("日聚合应=100+200+400=700,实际 %d", total)
	}

	otherKey, _ := SumCostToday(db, "", "k2", now.Format("2006-01-02T00:00:00Z"))
	if otherKey != 0 {
		t.Fatalf("其他 key 应为 0,实际 %d", otherKey)
	}
}
