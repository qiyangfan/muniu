// store 测试：迁移幂等、default 租户种子、repo 三方法，即查 key、批量落库幂等、日成本聚合。
package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
	"uuid"
)

func TestMigrate_幂等_重复打开不报错(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "muniu.db")

	db1, err := Open(p)
	if err != nil {
		t.Fatalf("首次打开失败: %v", err)
	}
	_ = db1.Close()

	db2, err := Open(p) // 第二次打开：已应用迁移必须全部跳过。
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
		uuid.NewV7().String(), "01900000-0000-7000-8000-000000000001",
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
		Estimated: false, CostCredits: 8800, Currency: "CNY",
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
			Status: status, Estimated: estimated, CostCredits: cost,
			LatencyTotalMs: 1, CreatedAt: at}
	}
	rows := []UsageRow{
		mk("a", "ok", false, 100, now),
		mk("b", "ok", false, 200, now),
		mk("c", "error", false, 400, now),                  // 失败请求也聚合，error_class 不影响成本口径。
		mk("d", "ok", true, 800, now),                      // estimated 行必须被剔除。
		mk("e", "ok", false, 1600, now.Add(-25*time.Hour)), // 昨日不计。
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

// openTestStore 开临时库，迁移自动执行。
func openTestStore(t *testing.T) *sql.DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "pricing.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// 动态价目 CRUD，迁移 0004：upsert 覆盖、窗口 JSON 往返、删除回落语义，删行即无。
func TestPricing_CRUD(t *testing.T) {
	db := openTestStore(t)
	rec := PricingRecord{
		Alias: "alias-a", Currency: "USD",
		InputCredits: 440_000, CachedInputCredits: 14_000, OutputCredits: 1_320_000,
		Timezone: "+08:00",
		Windows:  []PricingWindowRecord{{Window: "00:30-08:30", BasisPoints: 5000}},
	}
	if err := UpsertPricing(db, rec); err != nil {
		t.Fatal(err)
	}
	got, err := GetPricing(db, "alias-a")
	if err != nil {
		t.Fatal(err)
	}
	if got.Currency != "USD" || got.InputCredits != 440_000 || got.Timezone != "+08:00" ||
		len(got.Windows) != 1 || got.Windows[0] != (PricingWindowRecord{Window: "00:30-08:30", BasisPoints: 5000}) {
		t.Fatalf("读回不符: %+v", got)
	}
	// 覆盖写：窗口替换，cached 归零为缺省语义。
	rec.Windows = []PricingWindowRecord{{Window: "16:30-00:30", BasisPoints: 5000}, {Window: "00:00-00:30", BasisPoints: 5000}}
	rec.CachedInputCredits = 0
	if err := UpsertPricing(db, rec); err != nil {
		t.Fatal(err)
	}
	all, err := ListPricing(db)
	if err != nil || len(all) != 1 || len(all[0].Windows) != 2 || all[0].CachedInputCredits != 0 {
		t.Fatalf("覆盖写不符: %+v %v", all, err)
	}
	// 时区缺省补 UTC。
	rec2 := rec
	rec2.Alias = "alias-b"
	rec2.Timezone = ""
	if err := UpsertPricing(db, rec2); err != nil {
		t.Fatal(err)
	}
	got2, _ := GetPricing(db, "alias-b")
	if got2.Timezone != "UTC" {
		t.Fatalf("空时区应缺省 UTC: %+v", got2)
	}
	// 删除：行消失，再读 ErrNotFound。
	n, err := DeletePricing(db, "alias-a")
	if err != nil || n != 1 {
		t.Fatalf("删除应命中 1 行: %d %v", n, err)
	}
	if _, err := GetPricing(db, "alias-a"); err != ErrNotFound {
		t.Fatalf("删除后应 ErrNotFound: %v", err)
	}
}

// 毒丸行自愈，迁移 0005：损坏 payload 在单连接 MaxOpenConns(1) 下读取不得死锁，
// 损坏行被剔除并计入 skipped，合法行不受影响。
func TestPendingBufferedRows_毒丸行自愈(t *testing.T) {
	db := openTestStore(t)
	if _, err := db.Exec(`INSERT INTO metering_buffer (row_id, payload, created_at) VALUES ('bad','{broken',?)`,
		time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	// JSON 键 = UsageRow Go 字段名，encoding/json 匹配大小写不敏感。
	valid := `{"ID":"good","RequestID":"r1","Provider":"p","ModelAlias":"m","UpstreamModel":"m","Stream":false,"Status":"ok","PromptTokens":1,"CompletionTokens":2,"Estimated":false,"CostCredits":0,"Currency":"CNY","TenantID":"t","APIKeyID":"k","LatencyTotalMs":5,"CreatedAt":"2026-09-11T00:00:00Z"}`
	if _, err := db.Exec(`INSERT INTO metering_buffer (row_id, payload, created_at) VALUES ('good',?,?)`,
		valid, time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	rows, skipped, err := PendingBufferedRows(db, 10)
	if err != nil {
		t.Fatalf("毒丸不得导致错误(自愈剔除): %v", err)
	}
	if len(skipped) != 1 || len(rows) != 1 || rows[0].Row.ID != "good" {
		t.Fatalf("自愈结果不符: skipped=%v rows=%+v", skipped, rows)
	}
}
