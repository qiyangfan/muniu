package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func auditDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// 回归:结果集小于 limit 时不得越界切片(小结果集必现 panic)。
func TestListAdminAudit_结果不足一页不panic(t *testing.T) {
	db := auditDB(t)
	ctx := context.Background()
	for i, kind := range []string{"session", "federated"} {
		if err := AppendAdminAudit(ctx, db, AdminAudit{
			ID: string(rune('a' + i)), RequestID: "r", ActorKind: kind, ActorName: "n",
			Action: "a", Outcome: "ok", Detail: "{}", CreatedAt: "2026-11-20T00:00:0" + string(rune('0'+i)) + "Z",
		}); err != nil {
			t.Fatalf("append %s: %v", kind, err)
		}
	}
	items, next, err := ListAdminAudit(ctx, db, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || next != "" {
		t.Fatalf("应返回 2 行且无下一页: %d %q", len(items), next)
	}
	// 满页翻页仍工作:limit=1 时下一页游标非空
	items, next, err = ListAdminAudit(ctx, db, "", 1)
	if err != nil || len(items) != 1 || next == "" {
		t.Fatalf("翻页游标失效: %d %q %v", len(items), next, err)
	}
}

// 迁移 0020:审计值域接纳 federated(本地登录轨删除后 login 值域仅作历史兼容保留)。
func TestMigration0020_联邦审计值域(t *testing.T) {
	db := auditDB(t)
	ctx := context.Background()
	if err := AppendAdminAudit(ctx, db, AdminAudit{
		ID: "a1", RequestID: "r", ActorKind: "federated", ActorName: "张三",
		Action: "keys.post", Outcome: "ok", Detail: "{}", CreatedAt: "2026-11-20T00:00:00Z",
	}); err != nil {
		t.Fatalf("federated 审计行应可落库: %v", err)
	}
	if err := AppendAdminAudit(ctx, db, AdminAudit{
		ID: "a2", RequestID: "r", ActorKind: "bogus", ActorName: "x",
		Action: "a", Outcome: "ok", Detail: "{}", CreatedAt: "2026-11-20T00:00:00Z",
	}); err == nil {
		t.Fatal("非值域 actor_kind 必须被 CHECK 拒绝")
	}
}

// 迁移 0021:主体以Keycloak sub 为主键,同名展示名可并存;会话按 subject 归属。
func TestMigration0021_主体以subject为主键(t *testing.T) {
	db := auditDB(t)
	ctx := context.Background()
	first, err := ResolveAdminPrincipal(ctx, db, "keycloak:sub-1", "张三", "2026-11-21T00:00:00Z")
	if err != nil || first.Subject != "keycloak:sub-1" || first.Status != "active" {
		t.Fatalf("首登建档失败: %+v %v", first, err)
	}
	again, err := ResolveAdminPrincipal(ctx, db, "keycloak:sub-1", "张三", "2026-11-21T00:00:00Z")
	if err != nil || again.CreatedAt != first.CreatedAt {
		t.Fatalf("同一 sub 必须复用既有行: %+v %v", again, err)
	}
	if _, err := ResolveAdminPrincipal(ctx, db, "keycloak:sub-2", "张三", "2026-11-21T00:00:00Z"); err != nil {
		t.Fatalf("同名不同 sub 必须可并存(身份键是 sub 不是名字): %v", err)
	}

	// 会话仅对活跃主体可建;停用即删该主体会话
	if err := CreateAdminSession(ctx, db, "keycloak:sub-1", "h1", "2026-11-21T00:00:00Z", "2026-11-21T12:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if _, err := AdminSession(ctx, db, "h1", "2026-11-21T01:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := PatchAdminPrincipal(ctx, db, "keycloak:sub-1", true); err != nil {
		t.Fatal(err)
	}
	if _, err := AdminSession(ctx, db, "h1", "2026-11-21T01:00:00Z"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("停用后会话必须失效: %v", err)
	}
	if err := CreateAdminSession(ctx, db, "keycloak:sub-1", "h2", "2026-11-21T00:00:00Z", "2026-11-21T12:00:00Z"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("停用主体不得签发会话: %v", err)
	}
	if err := PatchAdminPrincipal(ctx, db, "keycloak:none", false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在主体应 ErrNotFound: %v", err)
	}
}

// 启动闸:旧 admin_users 含本地账密行必须明确拒绝,联邦行不得误伤。
func TestEnsureNoLocalAdminAccounts_拒本地账密行(t *testing.T) {
	db := auditDB(t)
	ctx := context.Background()
	if err := EnsureNoLocalAdminAccounts(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO admin_users(id,username,password_hash,status,created_at,origin)
		VALUES('u1','legacy-shadow','','active','2026-11-20T00:00:00Z','federated'),
		('u2','legacy-formal','$argon2id$y','active','2026-11-20T00:00:00Z','local')`); err != nil {
		t.Fatal(err)
	}
	err := EnsureNoLocalAdminAccounts(ctx, db)
	if !IsBusiness(err) || !strings.Contains(err.Error(), "legacy-formal") || strings.Contains(err.Error(), "legacy-shadow") {
		t.Fatalf("启动闸必须指名本地行且放行联邦行: %v", err)
	}
}

// 迁移 0020 重建审计表:既有审计行必须原样搬入新表(CHECK 扩值域不得丢行)。
func TestMigration0020_重建保留既有审计行(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "legacy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	// 直接验证重建语义:在全新库上删掉 0020 建的新表,按 0012 形态建旧表并插入,再跑一次 0020 DDL。
	if _, err := db.ExecContext(ctx, `DROP TABLE admin_audit_log`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE admin_audit_log (
		id TEXT PRIMARY KEY, request_id TEXT NOT NULL,
		actor_kind TEXT NOT NULL CHECK(actor_kind IN ('session','token','login')),
		actor_name TEXT NOT NULL, action TEXT NOT NULL, target_id TEXT,
		outcome TEXT NOT NULL, detail TEXT, created_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO admin_audit_log VALUES
		('old-1','r','session','alice','keys.post','k1','ok','{}','2026-11-19T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	body, err := migrationsFS.ReadFile("migrations/0020_admin_identity_federation.sql")
	if err != nil {
		t.Fatal(err)
	}
	// 只重放 DDL 中的审计表重建段(列已由迁移建好,ALTER 段不可重放)
	ddl := string(body)
	start := strings.Index(ddl, "CREATE TABLE admin_audit_log_rebuilt")
	if start < 0 {
		t.Fatal("0020 迁移缺重建段")
	}
	if _, err := db.ExecContext(ctx, ddl[start:]); err != nil {
		t.Fatalf("重建段执行失败: %v", err)
	}
	items, _, err := ListAdminAudit(ctx, db, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != "old-1" || items[0].ActorKind != "session" {
		t.Fatalf("重建后既有审计行丢失: %+v", items)
	}
	if err := AppendAdminAudit(ctx, db, AdminAudit{
		ID: "new-1", RequestID: "r", ActorKind: "federated", ActorName: "张三",
		Action: "a", Outcome: "ok", Detail: "{}", CreatedAt: "2026-11-20T00:00:00Z",
	}); err != nil {
		t.Fatalf("重建后 federated 应可落库: %v", err)
	}
}
