package auth

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/solvela/muniu/internal/store"
)

func newAdminTestService(t *testing.T) *AdminService {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "admin.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	svc, err := NewAdminService(context.Background(), db, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc.now = func() time.Time { return time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC) }
	return svc
}

// TestAdminAuth_会话生命周期 覆盖联邦首登建档、同一 sub 复用、注销幂等与过期惰性清理。
func TestAdminAuth_会话生命周期(t *testing.T) {
	svc := newAdminTestService(t)
	ctx := context.Background()
	token, p, err := svc.LoginFederated(ctx, "hufu-sub-alice", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if p.Subject != "hufu-sub-alice" || p.Status != "active" {
		t.Fatalf("subject 主键建档错误: %+v", p)
	}
	if got, err := svc.Session(ctx, token); err != nil || got.Subject != "hufu-sub-alice" {
		t.Fatal("会话校验", err)
	}
	if _, again, err := svc.LoginFederated(ctx, "hufu-sub-alice", "alice2"); err != nil || again.Subject != p.Subject {
		t.Fatalf("同一 sub 必须复用主体: %+v %v", again, err)
	}
	if principals, err := store.ListAdminPrincipals(ctx, svc.db); err != nil || len(principals) != 1 {
		t.Fatalf("同一 sub 只应有一个主体: %+v %v", principals, err)
	}
	if err := svc.Logout(ctx, token); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Session(ctx, token); !errors.Is(err, ErrAdminCredentials) {
		t.Fatal("注销未失效")
	}
	if err := svc.Logout(ctx, token); err != nil {
		t.Fatalf("重复注销应幂等: %v", err)
	}
	token, _, err = svc.LoginFederated(ctx, "hufu-sub-alice", "alice")
	if err != nil {
		t.Fatal(err)
	}
	svc.now = func() time.Time { return time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC) }
	if _, err := svc.Session(ctx, token); !errors.Is(err, ErrAdminCredentials) {
		t.Fatal("过期未拒绝")
	}
	var n int
	if err := svc.db.QueryRowContext(ctx, `SELECT count(*) FROM admin_sessions WHERE token_hash=?`,
		adminTokenHash(token)).Scan(&n); err != nil || n != 0 {
		t.Fatal("过期未惰性清理", n, err)
	}
}

// TestAdminAuth_停用即时失效且拒绝再登录 覆盖停用撤销会话、停用拒绝登录、启用不恢复旧会话。
func TestAdminAuth_停用即时失效且拒绝再登录(t *testing.T) {
	svc := newAdminTestService(t)
	ctx := context.Background()
	token, _, err := svc.LoginFederated(ctx, "hufu-sub-bob", "bob")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Patch(ctx, "hufu-sub-bob", true); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Session(ctx, token); !errors.Is(err, ErrAdminCredentials) {
		t.Fatal("停用后存量会话必须失效")
	}
	if _, _, err := svc.LoginFederated(ctx, "hufu-sub-bob", "bob"); !errors.Is(err, ErrAdminCredentials) {
		t.Fatal("停用主体必须拒绝登录")
	}
	if err := svc.Patch(ctx, "hufu-sub-bob", false); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Session(ctx, token); !errors.Is(err, ErrAdminCredentials) {
		t.Fatal("启用不得恢复旧会话")
	}
	if _, _, err := svc.LoginFederated(ctx, "hufu-sub-bob", "bob"); err != nil {
		t.Fatal("启用后应可重新登录", err)
	}
	if err := svc.Patch(ctx, "hufu-sub-missing", true); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("不存在主体应 ErrNotFound: %v", err)
	}
}

// TestAdminAuth_空subject拒绝 覆盖 RP 缺失 sub 的防御路径。
func TestAdminAuth_空subject拒绝(t *testing.T) {
	svc := newAdminTestService(t)
	for _, sub := range []string{"", "   "} {
		if _, _, err := svc.LoginFederated(context.Background(), sub, "x"); !errors.Is(err, ErrAdminCredentials) {
			t.Fatalf("空 sub 必须拒绝: %q %v", sub, err)
		}
	}
}

// TestAdminAuth_白名单外sub拒绝 覆盖「任意虎符用户都可进控制台」缺口。
func TestAdminAuth_白名单外sub拒绝(t *testing.T) {
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "allow.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	svc, err := NewAdminService(context.Background(), db, []string{"hufu-sub-ops"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, _, err := svc.LoginFederated(ctx, "hufu-sub-outsider", "路人"); !errors.Is(err, ErrAdminCredentials) {
		t.Fatalf("名单外 sub 必须拒绝: %v", err)
	}
	if _, _, err := svc.LoginFederated(ctx, "hufu-sub-ops", "运维"); err != nil {
		t.Fatalf("名单内 sub 应可登录: %v", err)
	}
	var n int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM admin_principals`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("拒绝路径不得建主体: %d %v", n, err)
	}
}

// TestPrincipalUsername_展示名收敛 覆盖控制字符剔除、UTF-8 边界截断与空名兜底。
func TestPrincipalUsername_展示名收敛(t *testing.T) {
	if got := principalUsername(" 张\n三\t "); got != "张三" {
		t.Fatalf("控制字符未剔除: %q", got)
	}
	if got := principalUsername(""); got != "虎符用户" {
		t.Fatalf("空名兜底错误: %q", got)
	}
	long := strings.Repeat("长", 100)
	got := principalUsername(long)
	if len(got) > 128 || !strings.HasPrefix(long, got) || strings.ContainsRune(got, '\uFFFD') {
		t.Fatalf("超长名未按 UTF-8 边界截断: %d %q", len(got), got)
	}
}
