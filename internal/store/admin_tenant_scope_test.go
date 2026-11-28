// 迁移 0023:范围列与绑定表;新主体默认 TENANT,存量主体升级 backfill GLOBAL。
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
	"uuid"
)

// 全量新库:默认 TENANT、CHECK 值域、绑定表主键与外键真实生效。
func TestMigration0023_新库默认TENANT与绑定约束(t *testing.T) {
	db := auditDB(t)
	ctx := context.Background()

	if _, err := ResolveAdminPrincipal(ctx, db, "keycloak:new", "新人", "2026-11-23T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	var scope string
	if err := db.QueryRowContext(ctx, `SELECT scope FROM admin_principals WHERE subject='keycloak:new'`).Scan(&scope); err != nil {
		t.Fatal(err)
	}
	if scope != "TENANT" {
		t.Fatalf("新主体默认范围应为 TENANT,实际 %q", scope)
	}
	if _, err := db.ExecContext(ctx, `UPDATE admin_principals SET scope='BOGUS' WHERE subject='keycloak:new'`); err == nil {
		t.Fatal("未知 scope 必须被 CHECK 拒绝")
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO admin_principal_tenants(subject,tenant_id)
		VALUES('keycloak:new','01900000-0000-7000-8000-000000000001')`); err != nil {
		t.Fatalf("绑定 default 租户失败: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO admin_principal_tenants(subject,tenant_id)
		VALUES('keycloak:new','01900000-0000-7000-8000-000000000001')`); err == nil {
		t.Fatal("重复绑定必须被主键拒绝")
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO admin_principal_tenants(subject,tenant_id)
		VALUES('keycloak:new','no-such-tenant')`); err == nil {
		t.Fatal("未知租户必须被外键拒绝")
	}
}

// 旧库(v22)升级:仅保留 0023 依赖的表,存量主体 backfill GLOBAL,升级后新插入仍默认 TENANT。
func TestMigration0023_升级backfill存量GLOBAL(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	stmts := []string{
		`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`,
		`CREATE TABLE tenants (id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE,
			status TEXT NOT NULL DEFAULT 'active', created_at TEXT NOT NULL)`,
		`INSERT INTO tenants(id,name,created_at) VALUES('t1','default','1970-01-01T00:00:00Z')`,
		`CREATE TABLE admin_principals (subject TEXT PRIMARY KEY, username TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'active', created_at TEXT NOT NULL, last_login_at TEXT)`,
		`INSERT INTO admin_principals(subject,username,created_at)
			VALUES('keycloak:old','老管理员','2026-11-20T00:00:00Z')`,
		`CREATE TABLE admin_audit_log (
			id TEXT PRIMARY KEY, request_id TEXT NOT NULL,
			actor_kind TEXT NOT NULL CHECK(actor_kind IN ('session','token','login','federated')),
			actor_name TEXT NOT NULL, action TEXT NOT NULL, target_id TEXT,
			outcome TEXT NOT NULL, detail TEXT, created_at TEXT NOT NULL)`,
		`CREATE TABLE key_cooldowns (
			provider TEXT NOT NULL, key_id TEXT NOT NULL, bucket TEXT NOT NULL,
			cool_until TEXT NOT NULL, consec INTEGER NOT NULL DEFAULT 0,
			ev_seq INTEGER NOT NULL DEFAULT 0, updated_at TEXT NOT NULL,
			PRIMARY KEY (provider, key_id))`,
	}
	for v := 1; v <= 22; v++ {
		stmts = append(stmts, fmt.Sprintf(
			`INSERT INTO schema_migrations(version,applied_at) VALUES(%d,'2026-11-20T00:00:00Z')`, v))
	}
	for _, s := range stmts {
		if _, err := db.ExecContext(ctx, s); err != nil {
			t.Fatalf("构造旧库失败 %q: %v", s, err)
		}
	}

	if err := migrate(ctx, db); err != nil {
		t.Fatalf("v22→v24 升级失败: %v", err)
	}
	var scope string
	if err := db.QueryRowContext(ctx, `SELECT scope FROM admin_principals WHERE subject='keycloak:old'`).Scan(&scope); err != nil {
		t.Fatal(err)
	}
	if scope != "GLOBAL" {
		t.Fatalf("存量主体应 backfill GLOBAL,实际 %q", scope)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO admin_principals(subject,username,created_at)
		VALUES('keycloak:new2','新人2','2026-11-23T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT scope FROM admin_principals WHERE subject='keycloak:new2'`).Scan(&scope); err != nil {
		t.Fatal(err)
	}
	if scope != "TENANT" {
		t.Fatalf("升级后新主体仍应默认 TENANT,实际 %q", scope)
	}
	var maxVersion int
	if err := db.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&maxVersion); err != nil {
		t.Fatal(err)
	}
	if maxVersion != 25 {
		t.Fatalf("迁移账应推进到 25,实际 %d", maxVersion)
	}
}

// 范围写入:整包替换、去重、空集撤销,以及三类业务拒绝与主体不存在。
func TestSetAdminPrincipalScope_整包替换与校验(t *testing.T) {
	db := openAdminDB(t)
	ctx := context.Background()
	if _, err := ResolveAdminPrincipal(ctx, db, "keycloak:s", "张三", "2026-11-23T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	const defaultTenant = "01900000-0000-7000-8000-000000000001"
	t2, err := CreateTenant(ctx, db, "租户二")
	if err != nil {
		t.Fatal(err)
	}
	t3, err := CreateTenant(ctx, db, "租户三")
	if err != nil {
		t.Fatal(err)
	}

	// 首登建档默认 TENANT,绑定为空。
	p, err := GetAdminPrincipal(ctx, db, "keycloak:s")
	if err != nil || p.Scope != "TENANT" {
		t.Fatalf("默认范围应为 TENANT: %+v %v", p, err)
	}

	// 授予 [default,t2] 后整包替换为 [t3],旧绑定立即不可见。
	if err := SetAdminPrincipalScope(ctx, db, "keycloak:s", "TENANT", []string{defaultTenant, t2}); err != nil {
		t.Fatal(err)
	}
	if ids, err := AdminPrincipalTenants(ctx, db, "keycloak:s"); err != nil || len(ids) != 2 {
		t.Fatalf("绑定应为 2 个: %v %v", ids, err)
	}
	if err := SetAdminPrincipalScope(ctx, db, "keycloak:s", "TENANT", []string{t3}); err != nil {
		t.Fatal(err)
	}
	if ids, _ := AdminPrincipalTenants(ctx, db, "keycloak:s"); len(ids) != 1 || ids[0] != t3 {
		t.Fatalf("整包替换失败: %v", ids)
	}

	// 重复 id 去重,空集撤销进入待授权态。
	if err := SetAdminPrincipalScope(ctx, db, "keycloak:s", "TENANT", []string{t3, t3}); err != nil {
		t.Fatal(err)
	}
	if ids, _ := AdminPrincipalTenants(ctx, db, "keycloak:s"); len(ids) != 1 {
		t.Fatalf("重复绑定应去重: %v", ids)
	}
	if err := SetAdminPrincipalScope(ctx, db, "keycloak:s", "TENANT", nil); err != nil {
		t.Fatal(err)
	}
	if ids, _ := AdminPrincipalTenants(ctx, db, "keycloak:s"); len(ids) != 0 {
		t.Fatalf("空集应清空绑定: %v", ids)
	}

	if err := SetAdminPrincipalScope(ctx, db, "keycloak:s", "GLOBAL", []string{t3}); !IsBusiness(err) {
		t.Fatalf("GLOBAL 带绑定应业务拒绝: %v", err)
	}
	if err := SetAdminPrincipalScope(ctx, db, "keycloak:s", "BOGUS", nil); !IsBusiness(err) {
		t.Fatalf("未知 scope 应业务拒绝: %v", err)
	}
	if err := SetAdminPrincipalScope(ctx, db, "keycloak:s", "TENANT", []string{"no-such"}); !IsBusiness(err) {
		t.Fatalf("未知租户应业务拒绝: %v", err)
	}
	if err := SetAdminPrincipalScope(ctx, db, "keycloak:none", "TENANT", nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("未知主体应 ErrNotFound: %v", err)
	}
}

// 主体列表回显 scope 与绑定;GLOBAL 主体的 tenant_ids 为空数组而非 null。
func TestListAdminPrincipals_回显范围与绑定(t *testing.T) {
	db := openAdminDB(t)
	ctx := context.Background()
	if _, err := ResolveAdminPrincipal(ctx, db, "keycloak:a", "甲", "2026-11-23T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveAdminPrincipal(ctx, db, "keycloak:b", "乙", "2026-11-23T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	const defaultTenant = "01900000-0000-7000-8000-000000000001"
	t2, err := CreateTenant(ctx, db, "租户二")
	if err != nil {
		t.Fatal(err)
	}
	if err := SetAdminPrincipalScope(ctx, db, "keycloak:a", "GLOBAL", nil); err != nil {
		t.Fatal(err)
	}
	if err := SetAdminPrincipalScope(ctx, db, "keycloak:b", "TENANT", []string{defaultTenant, t2}); err != nil {
		t.Fatal(err)
	}

	principals, err := ListAdminPrincipals(ctx, db)
	if err != nil || len(principals) != 2 {
		t.Fatalf("主体列表: %v %v", principals, err)
	}
	for _, p := range principals {
		switch p.Subject {
		case "keycloak:a":
			if p.Scope != "GLOBAL" || len(p.TenantIDs) != 0 {
				t.Fatalf("GLOBAL 主体回显不符: %+v", p)
			}
		case "keycloak:b":
			if p.Scope != "TENANT" || len(p.TenantIDs) != 2 {
				t.Fatalf("TENANT 主体回显不符: %+v", p)
			}
		default:
			t.Fatalf("意外主体: %+v", p)
		}
	}
}

// 租户集读侧:空集合失败关闭,显式集合只返回命中行。
func TestListTenantsByIDs_集合收敛(t *testing.T) {
	db := openAdminDB(t)
	ctx := context.Background()
	const defaultTenant = "01900000-0000-7000-8000-000000000001"
	t2, err := CreateTenant(ctx, db, "租户二")
	if err != nil {
		t.Fatal(err)
	}
	if ts, err := ListTenantsByIDs(ctx, db, nil); err != nil || len(ts) != 0 {
		t.Fatalf("空集合必须失败关闭: %v %v", ts, err)
	}
	if ts, err := ListTenantsByIDs(ctx, db, []string{t2}); err != nil || len(ts) != 1 || ts[0].ID != t2 {
		t.Fatalf("单租户收敛不符: %v %v", ts, err)
	}
	if ts, err := ListTenantsByIDs(ctx, db, []string{defaultTenant, t2}); err != nil || len(ts) != 2 {
		t.Fatalf("多租户并集不符: %v %v", ts, err)
	}
}

// 单事务补丁:范围/绑定/停用一次落库;任一步校验失败整单回滚(停用不得半生效)。
func TestPatchAdminPrincipalFields_单事务原子性(t *testing.T) {
	db := openAdminDB(t)
	ctx := context.Background()
	if _, err := ResolveAdminPrincipal(ctx, db, "keycloak:p", "补丁", "2026-11-23T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	t1, err := CreateTenant(ctx, db, "补丁一")
	if err != nil {
		t.Fatal(err)
	}
	scope := "TENANT"
	ids := []string{t1}
	disabled := true
	if err := PatchAdminPrincipalFields(ctx, db, "keycloak:p",
		AdminPrincipalPatch{Scope: &scope, TenantIDs: &ids, Disabled: &disabled}); err != nil {
		t.Fatal(err)
	}
	p, err := GetAdminPrincipal(ctx, db, "keycloak:p")
	if err != nil || p.Scope != "TENANT" || p.Status != "disabled" {
		t.Fatalf("三字段应一次生效: %+v %v", p, err)
	}
	if got, _ := AdminPrincipalTenants(ctx, db, "keycloak:p"); len(got) != 1 || got[0] != t1 {
		t.Fatalf("绑定应整包落库: %v", got)
	}

	// 恢复启用后以未知租户触发校验失败:停用与换绑都不得留下半生效。
	enabled := false
	if err := PatchAdminPrincipalFields(ctx, db, "keycloak:p", AdminPrincipalPatch{Disabled: &enabled}); err != nil {
		t.Fatal(err)
	}
	bad := []string{"no-such"}
	if err := PatchAdminPrincipalFields(ctx, db, "keycloak:p",
		AdminPrincipalPatch{TenantIDs: &bad, Disabled: &disabled}); !IsBusiness(err) {
		t.Fatalf("未知租户应业务拒绝: %v", err)
	}
	p, _ = GetAdminPrincipal(ctx, db, "keycloak:p")
	if p.Status != "active" {
		t.Fatalf("校验失败必须整单回滚,停用不得落库: %+v", p)
	}
	if got, _ := AdminPrincipalTenants(ctx, db, "keycloak:p"); len(got) != 1 || got[0] != t1 {
		t.Fatalf("校验失败不得改动既有绑定: %v", got)
	}
	if err := PatchAdminPrincipalFields(ctx, db, "keycloak:none",
		AdminPrincipalPatch{Disabled: &disabled}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("未知主体应 ErrNotFound: %v", err)
	}
}

// 并发补丁串行化:提升 GLOBAL 与整包换绑并发时,后者不得以陈旧 scope 覆盖前者。
func TestPatchAdminPrincipalFields_并发不丢更新(t *testing.T) {
	db := openAdminDB(t)
	ctx := context.Background()
	if _, err := ResolveAdminPrincipal(ctx, db, "keycloak:race", "并发", "2026-11-23T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	t2, err := CreateTenant(ctx, db, "并发租户")
	if err != nil {
		t.Fatal(err)
	}
	global := "GLOBAL"
	tenant := "TENANT"
	for i := 0; i < 20; i++ {
		empty := []string{}
		if err := PatchAdminPrincipalFields(ctx, db, "keycloak:race",
			AdminPrincipalPatch{Scope: &tenant, TenantIDs: &empty}); err != nil {
			t.Fatalf("轮次 %d 重置失败: %v", i, err)
		}
		ids := []string{t2}
		var wg sync.WaitGroup
		wg.Add(2)
		var scopeErr, bindErr error
		go func() {
			defer wg.Done()
			scopeErr = PatchAdminPrincipalFields(ctx, db, "keycloak:race",
				AdminPrincipalPatch{Scope: &global, TenantIDs: &empty})
		}()
		go func() {
			defer wg.Done()
			bindErr = PatchAdminPrincipalFields(ctx, db, "keycloak:race",
				AdminPrincipalPatch{TenantIDs: &ids})
		}()
		wg.Wait()
		if scopeErr != nil {
			t.Fatalf("轮次 %d 提升 GLOBAL 失败: %v", i, scopeErr)
		}
		// 绑定补丁先落时会合并出 GLOBAL+绑定,按契约 400 拒绝;后落则绑定被 GLOBAL+[] 覆盖。
		_ = bindErr
		p, err := GetAdminPrincipal(ctx, db, "keycloak:race")
		if err != nil {
			t.Fatal(err)
		}
		got, _ := AdminPrincipalTenants(ctx, db, "keycloak:race")
		if p.Scope != "GLOBAL" || len(got) != 0 {
			t.Fatalf("轮次 %d GLOBAL 意图被并发补丁覆盖: scope=%s bind=%v bindErr=%v",
				i, p.Scope, got, bindErr)
		}
	}
}

// 用量聚合:空集合失败关闭;多绑定并集只经一次 IN 过滤。
func TestUsageAggregateByTenants_集合收敛(t *testing.T) {
	db := openAdminDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	rows := []UsageRow{
		{ID: uuid.NewV7().String(), RequestID: "r1", TenantID: "t1", APIKeyID: "k1",
			Provider: "deepseek", ModelAlias: "m1", UpstreamModel: "u1", Status: "ok",
			PromptTokens: 10, CompletionTokens: 1, CostCredits: 100, Currency: "CNY",
			LatencyTotalMs: 1, CreatedAt: now},
		{ID: uuid.NewV7().String(), RequestID: "r2", TenantID: "t2", APIKeyID: "k2",
			Provider: "deepseek", ModelAlias: "m1", UpstreamModel: "u1", Status: "ok",
			PromptTokens: 20, CompletionTokens: 2, CostCredits: 200, Currency: "CNY",
			LatencyTotalMs: 1, CreatedAt: now},
		{ID: uuid.NewV7().String(), RequestID: "r3", TenantID: "t3", APIKeyID: "k3",
			Provider: "deepseek", ModelAlias: "m1", UpstreamModel: "u1", Status: "ok",
			PromptTokens: 30, CompletionTokens: 3, CostCredits: 300, Currency: "CNY",
			LatencyTotalMs: 1, CreatedAt: now},
	}
	if err := InsertUsageBatch(ctx, db, rows); err != nil {
		t.Fatal(err)
	}
	from := now.Add(-time.Minute).Format(time.RFC3339)
	to := now.Add(time.Minute).Format(time.RFC3339)

	if pts, err := UsageAggregateByTenants(ctx, db, DimDay, from, to, nil, ""); err != nil || len(pts) != 0 {
		t.Fatalf("空集合必须失败关闭: %v %v", pts, err)
	}
	pts, err := UsageAggregateByTenants(ctx, db, DimDay, from, to, []string{"t1"}, "")
	if err != nil || len(pts) != 1 || pts[0].Requests != 1 || pts[0].CostCredits != 100 {
		t.Fatalf("单租户聚合不符: %+v %v", pts, err)
	}
	pts, err = UsageAggregateByTenants(ctx, db, DimDay, from, to, []string{"t1", "t2"}, "")
	if err != nil || len(pts) != 1 || pts[0].Requests != 2 || pts[0].CostCredits != 300 {
		t.Fatalf("多绑定并集不符: %+v %v", pts, err)
	}
}
