package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
	"uuid"

	"github.com/solvela/muniu/internal/store"
)

// ErrAdminCredentials / ErrAdminLocked 为登录与会话拒绝分类，内部存储错误不混入认证错误。
var (
	ErrAdminCredentials = errors.New("管理员凭据无效")
	ErrAdminLocked      = errors.New("账号暂时锁定，请稍后重试")
)

type adminFailures struct {
	count int
	until time.Time
}

// AdminService 本地单进程登录锁定与会话服务；计数在重启后自然清零。
type AdminService struct {
	db        *sql.DB
	now       func() time.Time
	mu        sync.Mutex // 登录验证与计数串行，避免并发失败穿透阈值；管理面低频。
	failures  map[string]adminFailures
	dummyHash string
}

// NewAdminService 启动时清扫过期会话，失败拒绝装配。
func NewAdminService(ctx context.Context, db *sql.DB) (*AdminService, error) {
	if err := store.CleanAdminSessions(ctx, db, time.Now().UTC().Format(time.RFC3339)); err != nil {
		return nil, err
	}
	dummy, err := HashAdminPassword("invalid-account-dummy")
	if err != nil {
		return nil, err
	}
	return &AdminService{db: db, now: time.Now, failures: map[string]adminFailures{}, dummyHash: dummy}, nil
}

// Create API 与 CLI 共用用户名及口令校验。
func (s *AdminService) Create(ctx context.Context, username, password string) (store.AdminUser, error) {
	username = strings.TrimSpace(username)
	if !utf8.ValidString(username) {
		return store.AdminUser{}, store.MarkBusiness(fmt.Errorf("用户名须为有效 UTF-8 文本"))
	}
	if username == "" || len(username) > 128 || strings.ContainsAny(username, "\r\n\t") {
		return store.AdminUser{}, store.MarkBusiness(fmt.Errorf("用户名必填，最长 128 字节，不可含控制字符"))
	}
	hash, err := HashAdminPassword(password)
	if err != nil {
		return store.AdminUser{}, store.MarkBusiness(err)
	}
	u := store.AdminUser{
		ID: uuid.NewV7().String(), Username: username, PasswordHash: hash,
		Status: "active", CreatedAt: s.now().UTC().Format(time.RFC3339),
	}
	if err := store.CreateAdminUser(ctx, s.db, u); err != nil {
		return store.AdminUser{}, err
	}
	return u, nil
}

// Patch 改密或停用后旧会话即刻失效；启用不恢复旧会话。
func (s *AdminService) Patch(ctx context.Context, id string, disabled *bool, password string) error {
	hash := ""
	if password != "" {
		var err error
		hash, err = HashAdminPassword(password)
		if err != nil {
			return store.MarkBusiness(err)
		}
	}
	return store.PatchAdminUser(ctx, s.db, id, disabled, hash)
}

// Login 以真实账号累计失败；不存在账号仍计算等成本哈希，但不积累无界用户名状态。
func (s *AdminService) Login(ctx context.Context, username, password string) (string, store.AdminUser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	f := s.failures[username]
	if now.Before(f.until) {
		return "", store.AdminUser{}, ErrAdminLocked
	}
	if !f.until.IsZero() {
		f = adminFailures{}
	}
	u, err := store.GetAdminUser(ctx, s.db, username)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return "", u, err
	}
	hash := u.PasswordHash
	if hash == "" {
		hash = s.dummyHash
	}
	valid := VerifyAdminPassword(hash, password)
	// 不存在的账号不计失败:否则随机用户名会把锁定表撑成无界状态
	if errors.Is(err, store.ErrNotFound) {
		return "", store.AdminUser{}, ErrAdminCredentials
	}
	if !valid || u.Status != "active" {
		return "", store.AdminUser{}, s.recordFailure(username, f, now)
	}
	delete(s.failures, username)
	return s.openSession(ctx, u, now)
}

// LoginFederated 联邦轨:锦官 sub 映射本地影子账号并签发本地会话。身份信任来自 RP
// 验签后的 id_token,本轨不校验口令;停用账号拒绝,且不因再次登录自动恢复启用。
func (s *AdminService) LoginFederated(ctx context.Context, subject, name string) (string, store.AdminUser, error) {
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return "", store.AdminUser{}, ErrAdminCredentials
	}
	now := s.now().UTC()
	u, err := store.ResolveFederatedAdmin(ctx, s.db, uuid.NewV7().String(),
		subject, federatedUsername(name), now.Format(time.RFC3339))
	if err != nil {
		return "", store.AdminUser{}, err
	}
	if u.Status != "active" {
		return "", store.AdminUser{}, ErrAdminCredentials
	}
	return s.openSession(ctx, u, now)
}

// federatedUsername 锦官实名经本地用户名同一值域收敛:控符/换行拒绝,超长按 UTF-8
// 边界截断;空名兜底固定前缀,避免建出不可辨识账号。
func federatedUsername(name string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(name) {
		if r == '\r' || r == '\n' || r == '\t' || r < 0x20 || r == 0x7f {
			continue
		}
		b.WriteRune(r)
	}
	out := strings.TrimSpace(b.String())
	if !utf8.ValidString(out) {
		out = ""
	}
	for len(out) > 128 {
		_, size := utf8.DecodeLastRuneInString(out)
		out = out[:len(out)-size]
	}
	if out == "" {
		return "锦官用户"
	}
	return out
}

// loginMaxFailures 同一用户名连续失败达此数即锁定;锁定窗口固定 15 分钟。
const (
	loginMaxFailures = 5
	loginLockWindow  = 15 * time.Minute
)

// recordFailure 累计失败并返回对外统一错误;阈值处顺带设定锁定截止。
func (s *AdminService) recordFailure(username string, f adminFailures, now time.Time) error {
	f.count++
	if f.count >= loginMaxFailures {
		f.until = now.Add(loginLockWindow)
	}
	s.failures[username] = f
	return ErrAdminCredentials
}

// openSession 签发会话令牌；会话落库时用户已消失仍归凭据无效。
func (s *AdminService) openSession(ctx context.Context, u store.AdminUser, now time.Time) (
	string, store.AdminUser, error,
) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", u, err
	}
	token := base64.RawURLEncoding.EncodeToString(b)
	expires := now.Add(12 * time.Hour).Format(time.RFC3339)
	if err := store.CreateAdminSession(ctx, s.db, u, adminTokenHash(token),
		now.Format(time.RFC3339), expires); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return "", u, ErrAdminCredentials
		}
		return "", u, err
	}
	return token, u, nil
}

func adminTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Session 逐请求查服务端状态，账户失效、会话不存在与过期统一拒绝。
func (s *AdminService) Session(ctx context.Context, token string) (store.AdminUser, error) {
	if token == "" {
		return store.AdminUser{}, ErrAdminCredentials
	}
	u, err := store.AdminSession(ctx, s.db, adminTokenHash(token), s.now().UTC().Format(time.RFC3339))
	if errors.Is(err, store.ErrNotFound) {
		return u, ErrAdminCredentials
	}
	return u, err
}

// Logout 删除哈希记录，令牌明文不进入数据库。
func (s *AdminService) Logout(ctx context.Context, token string) error {
	return store.DeleteAdminSession(ctx, s.db, adminTokenHash(token))
}
