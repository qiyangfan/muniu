package auth

import (
	"encoding/base64"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/solvela/muniu/internal/store"
	"golang.org/x/crypto/argon2"
)

func TestAdminPassword_PHC与参数迁移(t *testing.T) {
	hash, err := HashAdminPassword("password123")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyAdminPassword(hash, "password123") || VerifyAdminPassword(hash, "wrong") {
		t.Fatal("口令校验错误")
	}
	salt := []byte("0123456789abcdef")
	key := argon2.IDKey([]byte("password123"), salt, 3, 20*1024, 1, 32)
	old := fmt.Sprintf("$argon2id$v=19$m=20480,t=3,p=1$%s$%s", base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key))
	if !VerifyAdminPassword(old, "password123") {
		t.Fatal("PHC 自带参数迁移失败")
	}
	for _, bad := range []string{"", "$argon2id$v=19$m=0,t=0,p=0$a$b", "$argon2id$v=19$m=999999999,t=1,p=1$a$b"} {
		if VerifyAdminPassword(bad, "x") {
			t.Fatal("畸形 PHC 被接受")
		}
	}
}

func TestAdminAuth_会话锁定生命周期(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "admin.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	svc, err := NewAdminService(db)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }
	user, err := svc.Create("alice", "password123")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, _, err := svc.Login("alice", "wrong"); !errors.Is(err, ErrAdminCredentials) {
			t.Fatal(err)
		}
	}
	if _, _, err := svc.Login("alice", "password123"); !errors.Is(err, ErrAdminLocked) {
		t.Fatal("锁定期正确口令未拒绝", err)
	}
	other, err := NewAdminService(db)
	if err != nil {
		t.Fatal(err)
	}
	other.now = svc.now
	if _, _, err := other.Login("alice", "password123"); err != nil {
		t.Fatal("进程重启未清锁", err)
	}
	now = now.Add(15 * time.Minute)
	token, _, err := svc.Login("alice", "password123")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := svc.Session(token); err != nil || got.Username != "alice" {
		t.Fatal("会话校验", err)
	}
	if err := svc.Patch(user.ID, nil, "newpassword"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Session(token); !errors.Is(err, ErrAdminCredentials) {
		t.Fatal("改密未注销会话", err)
	}
	token, _, err = svc.Login("alice", "newpassword")
	if err != nil {
		t.Fatal(err)
	}
	disabled := true
	if err := svc.Patch(user.ID, &disabled, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Session(token); !errors.Is(err, ErrAdminCredentials) {
		t.Fatal("停用未失效", err)
	}
	disabled = false
	if err := svc.Patch(user.ID, &disabled, ""); err != nil {
		t.Fatal(err)
	}
	token, _, err = svc.Login("alice", "newpassword")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Logout(token); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Session(token); !errors.Is(err, ErrAdminCredentials) {
		t.Fatal("注销未失效", err)
	}
	token, _, err = svc.Login("alice", "newpassword")
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(12 * time.Hour)
	if _, err := svc.Session(token); !errors.Is(err, ErrAdminCredentials) {
		t.Fatal("过期未拒绝", err)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM admin_sessions WHERE token_hash=?`, adminTokenHash(token)).Scan(&n); err != nil || n != 0 {
		t.Fatal("过期未惰性清理", n, err)
	}
}

func TestAdminAuth_不存在账号不占用锁定状态(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "limits.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	svc, err := NewAdminService(db)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		if _, _, err := svc.Login(fmt.Sprintf("missing-%d", i), "password123"); !errors.Is(err, ErrAdminCredentials) {
			t.Fatal(err)
		}
	}
	if len(svc.failures) != 0 {
		t.Fatalf("未认证随机用户名永久积累 %d 条锁定状态", len(svc.failures))
	}
}

func TestAdminAuth_CLI凭据必须可由JSON无损表达(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "utf8.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	svc, err := NewAdminService(db)
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range []struct{ username, password string }{
		{"alice", strings.Repeat(string([]byte{0xff}), 8)},
		{string([]byte{'a', 0xff}), "password123"},
	} {
		if _, err := svc.Create(in.username, in.password); err == nil {
			t.Fatal("CLI 接受了无法由 JSON 无损表达的凭据")
		}
	}
}
