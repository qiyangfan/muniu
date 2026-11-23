package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/solvela/muniu/internal/store"
)

// ErrAdminCredentials 会话/联邦登录拒绝的唯一分类;内部存储错误不混入认证错误。
var ErrAdminCredentials = errors.New("管理员凭据无效")

// AdminService 管理面会话服务。管理员主体由Keycloak(唯一账户权威)签发,本服务只做
// 「sub → 主体 → 会话」的本地投影,不持有任何口令或登录失败状态。
type AdminService struct {
	db  *sql.DB
	now func() time.Time
}

// FederatedAdminRole 管理面准入的唯一 Keycloak client role(与 platform realm 初始化一致)。
const FederatedAdminRole = "muniu-admin"

// NewAdminService 启动时清扫过期会话与登录中 state,失败拒绝装配。
func NewAdminService(ctx context.Context, db *sql.DB) (*AdminService, error) {
	if err := store.CleanAdminSessions(ctx, db, time.Now().UTC().Format(time.RFC3339)); err != nil {
		return nil, err
	}
	if err := store.CleanOIDCStates(ctx, db, time.Now().Add(-15*time.Minute).UTC().Format(time.RFC3339Nano)); err != nil {
		return nil, err
	}
	return &AdminService{db: db, now: time.Now}, nil
}

// LoginFederated 联邦轨:验签后的 sub 作为主体键建/取主体并签发本地会话。
// 准入要求 client roles 含 FederatedAdminRole;本轨不校验口令;停用主体拒绝且不自动恢复。
func (s *AdminService) LoginFederated(
	ctx context.Context, subject, name string, roles []string,
) (string, store.AdminPrincipal, error) {
	subject = strings.TrimSpace(subject)
	if subject == "" || !hasFederatedAdminRole(roles) {
		return "", store.AdminPrincipal{}, ErrAdminCredentials
	}
	now := s.now().UTC()
	p, err := store.ResolveAdminPrincipal(ctx, s.db, subject, principalUsername(name),
		now.Format(time.RFC3339))
	if err != nil {
		return "", store.AdminPrincipal{}, err
	}
	if p.Status != "active" {
		return "", store.AdminPrincipal{}, ErrAdminCredentials
	}
	return s.openSession(ctx, p, now)
}

func hasFederatedAdminRole(roles []string) bool {
	for _, role := range roles {
		if role == FederatedAdminRole {
			return true
		}
	}
	return false
}

// Patch 启停主体;停用即撤销该主体全部会话,启用不恢复旧会话。
func (s *AdminService) Patch(ctx context.Context, subject string, disabled bool) error {
	return store.PatchAdminPrincipal(ctx, s.db, subject, disabled)
}

// principalUsername 实名经展示名同一值域收敛:控符/换行剔除,超长按 UTF-8 边界截断;
// range 字符串已把非法字节规整为 U+FFFD,无需另做 UTF-8 校验;
// 空名兜底固定前缀,避免列表出现不可辨识行。
func principalUsername(name string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(name) {
		if r < 0x20 || r == 0x7f {
			continue
		}
		b.WriteRune(r)
	}
	out := strings.TrimSpace(b.String())
	for len(out) > 128 {
		out = out[:lastRuneStart(out)]
	}
	if out == "" {
		return "Keycloak用户"
	}
	return out
}

// lastRuneStart 返回末字符起始字节,按 UTF-8 边界截断不产生非法串;
// 输入来自 RP 验签后的 name,仍可能含非法字节(Go range 产出 U+FFFD),此路径一并消除。
func lastRuneStart(s string) int {
	for i := len(s) - 1; i > 0; i-- {
		if s[i]&0xC0 != 0x80 {
			return i
		}
	}
	return 0
}

// openSession 签发会话令牌;会话落库时主体已停用则归凭据无效。
func (s *AdminService) openSession(ctx context.Context, p store.AdminPrincipal, now time.Time) (
	string, store.AdminPrincipal, error,
) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", p, err
	}
	token := base64.RawURLEncoding.EncodeToString(b)
	expires := now.Add(12 * time.Hour).Format(time.RFC3339)
	if err := store.CreateAdminSession(ctx, s.db, p.Subject, adminTokenHash(token),
		now.Format(time.RFC3339), expires); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return "", p, ErrAdminCredentials
		}
		return "", p, err
	}
	return token, p, nil
}

func adminTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Session 逐请求查服务端状态:主体停用、会话不存在与过期统一拒绝。
func (s *AdminService) Session(ctx context.Context, token string) (store.AdminPrincipal, error) {
	if token == "" {
		return store.AdminPrincipal{}, ErrAdminCredentials
	}
	p, err := store.AdminSession(ctx, s.db, adminTokenHash(token), s.now().UTC().Format(time.RFC3339))
	if errors.Is(err, store.ErrNotFound) {
		return p, ErrAdminCredentials
	}
	return p, err
}

// Logout 删除哈希记录,令牌明文不进入数据库。
func (s *AdminService) Logout(ctx context.Context, token string) error {
	return store.DeleteAdminSession(ctx, s.db, adminTokenHash(token))
}
