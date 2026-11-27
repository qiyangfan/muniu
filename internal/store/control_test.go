// 控制面本地台账：快照投影原子写、版本单调、命令幂等与状态计数。
package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// openControl 以真实 sql.DB 打开临时库。
func openControl(t *testing.T) *sql.DB {
	t.Helper()
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "control-store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestControlState_原子投影与版本单调(t *testing.T) {
	db := openControl(t)
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339)
	st := ControlState{
		Snapshot: ControlSnapshotRecord{
			ServiceID: "muniu", Version: 2, ETag: `"v2"`, ObservedAt: now,
			Signature: "sig", Payload: []byte(`{"version":2}`), UpdatedAt: now,
		},
		ReplacePricing: true,
		Pricing: []PricingRecord{{
			Alias: "alias-a", Currency: "CNY", InputCredits: 1, OutputCredits: 2,
			Timezone: "UTC", UpdatedAt: now,
		}},
		Tenants: []ControlTenantRecord{{
			ID: "t2", Name: "acme", Status: "active", DailyQuotaCredits: 10, TPMLimit: 20,
		}},
		Audit: &AdminAudit{
			ID: "a1", RequestID: "a1", ActorKind: "system", ActorName: "control-snapshot",
			Action: "snapshot.apply", Outcome: "ok", Detail: "{}", CreatedAt: now,
		},
	}
	if err := ApplyControlState(ctx, db, st); err != nil {
		t.Fatal(err)
	}
	var pricingN, auditN int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM model_pricing`).Scan(&pricingN); err != nil || pricingN != 1 {
		t.Fatalf("价目投影不符: %d %v", pricingN, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM admin_audit_log WHERE actor_kind='system'`).Scan(&auditN); err != nil || auditN != 1 {
		t.Fatalf("系统审计不符: %d %v", auditN, err)
	}
	var status string
	if err := db.QueryRowContext(ctx, `SELECT status FROM tenants WHERE name='default'`).Scan(&status); err != nil || status != "disabled" {
		t.Fatalf("快照缺席租户应停用: %q %v", status, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT status FROM tenants WHERE id='t2'`).Scan(&status); err != nil || status != "active" {
		t.Fatalf("快照租户应写入: %q %v", status, err)
	}
	rec, err := LoadControlSnapshot(ctx, db, "muniu")
	if err != nil || rec.Version != 2 || string(rec.Payload) != `{"version":2}` {
		t.Fatalf("last-known 不符: %+v %v", rec, err)
	}
	// 旧版本不得覆盖 last-known（审计 ID 唯一，后续轮次不再写审计）
	st.Audit = nil
	st.Snapshot.Version = 1
	if err := ApplyControlState(ctx, db, st); err != nil {
		t.Fatal(err)
	}
	rec, _ = LoadControlSnapshot(ctx, db, "muniu")
	if rec.Version != 2 {
		t.Fatalf("旧版本不得覆盖: %d", rec.Version)
	}
	// 显式空价目清空投影
	st.Snapshot.Version = 3
	st.Pricing = nil
	if err := ApplyControlState(ctx, db, st); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM model_pricing`).Scan(&pricingN); err != nil || pricingN != 0 {
		t.Fatalf("显式空价目未清空: %d %v", pricingN, err)
	}
}

func TestControlCommands_幂等与状态(t *testing.T) {
	db := openControl(t)
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339)
	row := ControlCommandRow{
		ID: "c1", Action: "cache_purge", ActionSchemaVersion: 1, Params: `{"all":true}`,
		Source: "control", Status: "pending", CreatedAt: now,
	}
	inserted, err := InsertControlCommand(ctx, db, row)
	if err != nil || !inserted {
		t.Fatalf("首次插入失败: %v %v", inserted, err)
	}
	if inserted, err := InsertControlCommand(ctx, db, row); err != nil || inserted {
		t.Fatalf("重复命令应幂等: %v %v", inserted, err)
	}
	if err := UpdateControlCommandResult(ctx, db, "c1", "acked", "purged=1", now); err != nil {
		t.Fatal(err)
	}
	got, err := GetControlCommand(ctx, db, "c1")
	if err != nil || got.Status != "acked" || got.Result != "purged=1" {
		t.Fatalf("台账更新不符: %+v %v", got, err)
	}
	if n, err := BumpControlCommandAttempts(ctx, db, "c1"); err != nil || n != 1 {
		t.Fatalf("重试计数不符: %d %v", n, err)
	}
	list, err := ListControlCommands(ctx, db, 10)
	if err != nil || len(list) != 1 {
		t.Fatalf("列表不符: %d %v", len(list), err)
	}
	stats, err := CountControlCommandsByStatus(ctx, db)
	if err != nil || stats["acked"] != 1 {
		t.Fatalf("状态计数不符: %v %v", stats, err)
	}
	if _, err := GetControlCommand(ctx, db, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("缺失命令应 ErrNotFound: %v", err)
	}
}
