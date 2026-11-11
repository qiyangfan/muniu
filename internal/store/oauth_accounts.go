// OAuth 订阅账号存取(oauth-subscription-lane 契约):账号 CRUD、刷新回写、状态迁移。
// 凭据明文落库(无法哈希:刷新需原文使用),调用方负责日志脱敏;行状态即账号生命周期
// 单源,池内凭据是投影,重启后由装配层按行重建。
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// OAuth 账号状态值域:active 可调度;auth_failed 终态(刷新被拒,重新授权恢复)。
const (
	OAuthStatusActive     = "active"
	OAuthStatusAuthFailed = "auth_failed"
)

// OAuthAccountRow 账号表行形态(存储侧单源,oauth/admin/装配层共用投影)。
type OAuthAccountRow struct {
	ID           string
	ProviderType string
	Status       string
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
	StatusReason string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// ErrOAuthAccountNotFound 账号不存在(装配校验与刷新器「运行中删除」判定共用)。
var ErrOAuthAccountNotFound = errors.New("store: oauth 账号不存在")

// ErrOAuthAccountExists 账号 id 冲突(主键唯一约束;管理面映射 409,其余 DB 错误映射 500,
// 防磁盘满等故障误报为「已存在」)。
var ErrOAuthAccountExists = errors.New("store: oauth 账号已存在")

func oauthTime(t time.Time) string { return t.UTC().Truncate(time.Second).Format(time.RFC3339) }

func scanOAuthAccount(row interface{ Scan(...any) error }) (OAuthAccountRow, error) {
	var r OAuthAccountRow
	var exp, created, updated string
	err := row.Scan(&r.ID, &r.ProviderType, &r.Status, &r.AccessToken, &r.RefreshToken,
		&exp, &r.StatusReason, &created, &updated)
	if err != nil {
		return r, err
	}
	if r.ExpiresAt, err = time.Parse(time.RFC3339, exp); err != nil {
		return r, fmt.Errorf("store: oauth expires_at 形态: %w", err)
	}
	if r.CreatedAt, err = time.Parse(time.RFC3339, created); err != nil {
		return r, fmt.Errorf("store: oauth created_at 形态: %w", err)
	}
	if r.UpdatedAt, err = time.Parse(time.RFC3339, updated); err != nil {
		return r, fmt.Errorf("store: oauth updated_at 形态: %w", err)
	}
	return r, nil
}

const oauthAccountCols = `id, provider_type, status, access_token, refresh_token, expires_at, status_reason, created_at, updated_at`

// CreateOAuthAccount 新建账号;id 冲突显式报错(账号 ID 是配置引用主键,静默覆盖会让
// 两份配置指向同一行而各自以为独占)。
func CreateOAuthAccount(db *sql.DB, r OAuthAccountRow) error {
	_, err := db.Exec(`INSERT INTO oauth_accounts
		(id, provider_type, status, access_token, refresh_token, expires_at, status_reason, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?)`,
		r.ID, r.ProviderType, r.Status, r.AccessToken, r.RefreshToken, oauthTime(r.ExpiresAt),
		r.StatusReason, oauthTime(r.CreatedAt), oauthTime(r.UpdatedAt))
	if err != nil {
		// 唯一键冲突单独成哨兵(admin_users 先例):其余 DB 故障不得伪装成「已存在」
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return ErrOAuthAccountExists
		}
		return fmt.Errorf("store: 建 oauth 账号 %s: %w", r.ID, err)
	}
	return nil
}

// GetOAuthAccount 单账号;不存在返回 ErrOAuthAccountNotFound。
func GetOAuthAccount(db *sql.DB, id string) (OAuthAccountRow, error) {
	row := db.QueryRow(`SELECT `+oauthAccountCols+` FROM oauth_accounts WHERE id=?`, id)
	r, err := scanOAuthAccount(row)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrOAuthAccountNotFound
	}
	if err != nil {
		return r, fmt.Errorf("store: 读 oauth 账号 %s: %w", id, err)
	}
	return r, nil
}

// ListOAuthAccounts 全量账号,created_at 升序(列表页稳定序)。
func ListOAuthAccounts(db *sql.DB) ([]OAuthAccountRow, error) {
	rows, err := db.Query(`SELECT ` + oauthAccountCols + ` FROM oauth_accounts ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("store: 读 oauth 账号列表: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []OAuthAccountRow
	for rows.Next() {
		r, serr := scanOAuthAccount(rows)
		if serr != nil {
			return nil, fmt.Errorf("store: 扫 oauth 账号: %w", serr)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// UpdateOAuthAccountTokens 刷新成功回写:换新凭据对与过期时刻,状态回 active 并清原因
// (auth_failed 账号经重新授权恢复走同一入口)。账号运行中被删返回 ErrOAuthAccountNotFound。
func UpdateOAuthAccountTokens(db *sql.DB, id, accessToken, refreshToken string, expiresAt time.Time, now time.Time) error {
	res, err := db.Exec(`UPDATE oauth_accounts SET
		access_token=?, refresh_token=?, expires_at=?, status=?, status_reason='', updated_at=?
		WHERE id=?`,
		accessToken, refreshToken, oauthTime(expiresAt), OAuthStatusActive, oauthTime(now), id)
	if err != nil {
		return fmt.Errorf("store: 回写 oauth 凭据 %s: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrOAuthAccountNotFound
	}
	return nil
}

// SetOAuthAccountStatus 状态迁移(刷新被拒 → auth_failed 终态等);账号不存在返回 ErrOAuthAccountNotFound。
func SetOAuthAccountStatus(db *sql.DB, id, status, reason string, now time.Time) error {
	res, err := db.Exec(`UPDATE oauth_accounts SET status=?, status_reason=?, updated_at=? WHERE id=?`,
		status, reason, oauthTime(now), id)
	if err != nil {
		return fmt.Errorf("store: 迁移 oauth 状态 %s: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrOAuthAccountNotFound
	}
	return nil
}

// DeleteOAuthAccount 删账号;引用校验在管理面(读拓扑做引用判定),存储层保持无配置知识。
func DeleteOAuthAccount(db *sql.DB, id string) error {
	res, err := db.Exec(`DELETE FROM oauth_accounts WHERE id=?`, id)
	if err != nil {
		return fmt.Errorf("store: 删 oauth 账号 %s: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrOAuthAccountNotFound
	}
	return nil
}
