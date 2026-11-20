package store

import (
	"context"
	"database/sql"
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

// 迁移 0020:审计值域接纳 federated;admin_users 只增列且联邦 sub 唯一。
func TestMigration0020_联邦身份列与审计值域(t *testing.T) {
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

	// 本地账号默认 origin=local 且 external_subject 为空
	if err := CreateAdminUser(ctx, db, AdminUser{
		ID: "u1", Username: "local", PasswordHash: "h", Status: "active", CreatedAt: "2026-11-20T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	u, err := GetAdminUser(ctx, db, "local")
	if err != nil || u.Origin != "local" || u.ExternalSubject != "" {
		t.Fatalf("本地账号默认值错误: %+v %v", u, err)
	}

	// 联邦影子账号:同一 sub 唯一
	fed, err := ResolveFederatedAdmin(ctx, db, "u2", "sub-1", "张三", "2026-11-20T00:00:00Z")
	if err != nil || fed.Origin != "federated" || fed.ExternalSubject != "sub-1" {
		t.Fatalf("联邦建号失败: %+v %v", fed, err)
	}
	again, err := ResolveFederatedAdmin(ctx, db, "u3", "sub-1", "张三", "2026-11-20T00:00:00Z")
	if err != nil || again.ID != fed.ID {
		t.Fatalf("同一 sub 必须复用既有行: %+v %v", again, err)
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
