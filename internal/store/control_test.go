// 控制面本地台账：快照投影原子写、版本单调、命令幂等与状态计数。
package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const seedDefaultTenantID = "01900000-0000-7000-8000-000000000001"

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

func TestControlState_同名不同ID无引用自动对账(t *testing.T) {
	db := openControl(t)
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339)
	st := ControlState{
		Snapshot: ControlSnapshotRecord{
			ServiceID: "muniu", Version: 1, ObservedAt: now,
			Signature: "sig", Payload: []byte(`{"version":1}`), UpdatedAt: now,
		},
		Tenants: []ControlTenantRecord{{
			ID: "t-control", Name: "default", Status: "active", DailyQuotaCredits: 1, TPMLimit: 1,
		}},
	}
	if err := ApplyControlState(ctx, db, st); err != nil {
		t.Fatalf("无引用的同名种子应可对账删除: %v", err)
	}
	var id string
	if err := db.QueryRowContext(ctx, `SELECT id FROM tenants WHERE name='default'`).Scan(&id); err != nil || id != "t-control" {
		t.Fatalf("default 应归属控制面 ID: %q %v", id, err)
	}
	var n int64
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tenants WHERE id=?`, seedDefaultTenantID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("本地种子残留应删除: %d %v", n, err)
	}
}

func TestControlState_同名不同ID有引用显式拒绝(t *testing.T) {
	db := openControl(t)
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.ExecContext(ctx, `INSERT INTO api_keys
		(id, tenant_id, name, key_hash, prefix, created_at) VALUES (?,?,?,?,?,?)`,
		"k-local", seedDefaultTenantID, "local-key", "hash-local", "mk-local0001", now); err != nil {
		t.Fatal(err)
	}
	st := ControlState{
		Snapshot: ControlSnapshotRecord{
			ServiceID: "muniu", Version: 2, ObservedAt: now,
			Signature: "sig", Payload: []byte(`{"version":2}`), UpdatedAt: now,
		},
		Tenants: []ControlTenantRecord{{ID: "t-control", Name: "default", Status: "active"}},
	}
	err := ApplyControlState(ctx, db, st)
	if err == nil || !strings.Contains(err.Error(), "租户身份冲突") {
		t.Fatalf("有引用的同名租户必须显式拒绝: %v", err)
	}
	if _, err := LoadControlSnapshot(ctx, db, "muniu"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("冲突时快照必须整体回滚: %v", err)
	}
	var local int64
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tenants WHERE id=?`, seedDefaultTenantID).Scan(&local); err != nil || local != 1 {
		t.Fatalf("本地租户不得被删除: %d %v", local, err)
	}
}

func TestControlState_图片价目整表替换(t *testing.T) {
	db := openControl(t)
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339)
	st := ControlState{
		Snapshot: ControlSnapshotRecord{
			ServiceID: "muniu", Version: 1, ObservedAt: now,
			Signature: "sig", Payload: []byte(`{"version":1}`), UpdatedAt: now,
		},
		ReplaceImagePricing: true,
		ImagePricing: []ImagePricingRecord{{
			Alias: "img-a", Currency: "CNY", DefaultCredits: 1,
			Tiers: map[string]int64{"1024x1024|standard": 2}, UpdatedAt: now,
		}},
	}
	if err := ApplyControlState(ctx, db, st); err != nil {
		t.Fatal(err)
	}
	var cur string
	var def int64
	if err := db.QueryRowContext(ctx,
		`SELECT currency, default_credits FROM image_pricing WHERE alias='img-a'`).Scan(&cur, &def); err != nil || cur != "CNY" || def != 1 {
		t.Fatalf("图片价目投影不符: %s %d %v", cur, def, err)
	}
	st.Snapshot.Version = 2
	st.ImagePricing = nil
	if err := ApplyControlState(ctx, db, st); err != nil {
		t.Fatal(err)
	}
	var n int64
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM image_pricing`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("显式空图片价目应清空: %d %v", n, err)
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
