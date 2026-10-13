package auth

import (
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

	"github.com/google/uuid"
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
func NewAdminService(db *sql.DB) (*AdminService, error) {
	if err := store.CleanAdminSessions(db, time.Now().UTC().Format(time.RFC3339)); err != nil {
		return nil, err
	}
	dummy, err := HashAdminPassword("invalid-account-dummy")
	if err != nil {
		return nil, err
	}
	return &AdminService{db: db, now: time.Now, failures: map[string]adminFailures{}, dummyHash: dummy}, nil
}

// Create API 与 CLI 共用用户名及口令校验。
func (s *AdminService) Create(username, password string) (store.AdminUser, error) {
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
	id, err := uuid.NewV7()
	if err != nil {
		return store.AdminUser{}, err
	}
	u := store.AdminUser{ID: id.String(), Username: username, PasswordHash: hash, Status: "active", CreatedAt: s.now().UTC().Format(time.RFC3339)}
	if err := store.CreateAdminUser(s.db, u); err != nil {
		return store.AdminUser{}, err
	}
	return u, nil
}

// Patch 改密或停用后旧会话即刻失效；启用不恢复旧会话。
func (s *AdminService) Patch(id string, disabled *bool, password string) error {
	hash := ""
	if password != "" {
		var err error
		hash, err = HashAdminPassword(password)
		if err != nil {
			return store.MarkBusiness(err)
		}
	}
	return store.PatchAdminUser(s.db, id, disabled, hash)
}

// Login 以真实账号累计失败；不存在账号仍计算等成本哈希，但不积累无界用户名状态。
func (s *AdminService) Login(username, password string) (string, store.AdminUser, error) {
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
	u, err := store.GetAdminUser(s.db, username)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return "", u, err
	}
	hash := u.PasswordHash
	if hash == "" {
		hash = s.dummyHash
	}
	valid := VerifyAdminPassword(hash, password)
	if errors.Is(err, store.ErrNotFound) {
		return "", store.AdminUser{}, ErrAdminCredentials
	}
	if !valid || u.Status != "active" {
		f.count++
		if f.count >= 5 {
			f.until = now.Add(15 * time.Minute)
		}
		s.failures[username] = f
		return "", store.AdminUser{}, ErrAdminCredentials
	}
	delete(s.failures, username)
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", u, err
	}
	token := base64.RawURLEncoding.EncodeToString(b)
	if err := store.CreateAdminSession(s.db, u, adminTokenHash(token), now.Format(time.RFC3339), now.Add(12*time.Hour).Format(time.RFC3339)); err != nil {
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
func (s *AdminService) Session(token string) (store.AdminUser, error) {
	if token == "" {
		return store.AdminUser{}, ErrAdminCredentials
	}
	u, err := store.AdminSession(s.db, adminTokenHash(token), s.now().UTC().Format(time.RFC3339))
	if errors.Is(err, store.ErrNotFound) {
		return u, ErrAdminCredentials
	}
	return u, err
}

// Logout 删除哈希记录，令牌明文不进入数据库。
func (s *AdminService) Logout(token string) error {
	return store.DeleteAdminSession(s.db, adminTokenHash(token))
}
