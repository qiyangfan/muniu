package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// AdminUser 的哈希永不经 JSON 回显。
type AdminUser struct {
	ID              string  `json:"id"`
	Username        string  `json:"username"`
	PasswordHash    string  `json:"-"`
	Status          string  `json:"status"`
	CreatedAt       string  `json:"created_at"`
	LastLoginAt     *string `json:"last_login_at"`
	Origin          string  `json:"origin"`
	ExternalSubject string  `json:"external_subject,omitempty"`
}

// adminUserCols 认证域读取列清单;origin 决定会话归因轨(session/federated)。
const adminUserCols = `id,username,password_hash,status,created_at,last_login_at,origin,coalesce(external_subject,'')`

// CreateAdminUser 用户名冲突以业务错误返回。
func CreateAdminUser(ctx context.Context, db *sql.DB, u AdminUser) error {
	_, err := db.ExecContext(ctx, `INSERT INTO admin_users(id,username,password_hash,created_at) VALUES(?,?,?,?)`,
		u.ID, u.Username, u.PasswordHash, u.CreatedAt)
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed") {
		return MarkBusiness(fmt.Errorf("管理员用户名已存在"))
	}
	return err
}

// GetAdminUser 按用户名读取，仅认证域消费哈希。
func GetAdminUser(ctx context.Context, db *sql.DB, username string) (AdminUser, error) {
	var u AdminUser
	err := db.QueryRowContext(ctx, `SELECT `+adminUserCols+` FROM admin_users WHERE username=?`, username).
		Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Status, &u.CreatedAt, &u.LastLoginAt,
			&u.Origin, &u.ExternalSubject)
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNotFound
	}
	return u, err
}

// ListAdminUsers 公开形态不读取哈希。
func ListAdminUsers(ctx context.Context, db *sql.DB) ([]AdminUser, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id,username,status,created_at,last_login_at,origin,coalesce(external_subject,'')
		 FROM admin_users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []AdminUser{}
	for rows.Next() {
		var u AdminUser
		if err := rows.Scan(&u.ID, &u.Username, &u.Status, &u.CreatedAt, &u.LastLoginAt,
			&u.Origin, &u.ExternalSubject); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// HasActiveAdmins 装配期判断是否开放管理面。
func HasActiveAdmins(ctx context.Context, db *sql.DB) (bool, error) {
	var n int
	err := db.QueryRowContext(ctx, `SELECT count(*) FROM admin_users WHERE status='active'`).Scan(&n)
	return n > 0, err
}

// PatchAdminUser 账号变更与存量会话删除同事务，改密/停用无短暂旧会话窗口。
func PatchAdminUser(ctx context.Context, db *sql.DB, id string, disabled *bool, hash string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := applyAdminUserPatch(ctx, tx, id, disabled, hash); err != nil {
		return err
	}
	return tx.Commit()
}

// applyAdminUserPatch 在事务内改状态/改密并按需删存量会话；账号不存在返回 ErrNotFound。
func applyAdminUserPatch(ctx context.Context, tx *sql.Tx, id string, disabled *bool, hash string) error {
	var old string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM admin_users WHERE id=?`, id).Scan(&old); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if err := updateAdminUserColumns(ctx, tx, id, disabled, hash); err != nil {
		return err
	}
	// 改密与停用都必须让存量会话立即失效，防止旧凭据在改密窗口内继续可用。
	if hash != "" || disabled != nil && *disabled {
		if _, err := tx.ExecContext(ctx, `DELETE FROM admin_sessions WHERE user_id=?`, id); err != nil {
			return err
		}
	}
	return nil
}

// updateAdminUserColumns 按补丁更新状态列/密码哈希列，nil 或空串表示该列不动。
func updateAdminUserColumns(ctx context.Context, tx *sql.Tx, id string, disabled *bool, hash string) error {
	if disabled != nil {
		if _, err := tx.ExecContext(ctx,
			`UPDATE admin_users SET status=? WHERE id=?`, adminUserStatus(*disabled), id); err != nil {
			return err
		}
	}
	if hash != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE admin_users SET password_hash=? WHERE id=?`, hash, id); err != nil {
			return err
		}
	}
	return nil
}

// adminUserStatus 停用标志到行状态值的映射；停用账号的存量会话随后即被清空。
func adminUserStatus(disabled bool) string {
	if disabled {
		return "disabled"
	}
	return "active"
}

// CreateAdminSession 登录校验与签发之间若停用或改密，条件插入拒绝过时的凭据。
func CreateAdminSession(ctx context.Context, db *sql.DB, u AdminUser, hash, created, expires string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `INSERT INTO admin_sessions(token_hash,user_id,created_at,expires_at)
		SELECT ?,id,?,? FROM admin_users WHERE id=? AND status='active' AND password_hash=?`,
		hash, created, expires, u.ID, u.PasswordHash)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `UPDATE admin_users SET last_login_at=? WHERE id=?`, created, u.ID); err != nil {
		return err
	}
	return tx.Commit()
}

// AdminSession 每次查活跃账号，过期行惰性清理。
func AdminSession(ctx context.Context, db *sql.DB, hash, now string) (AdminUser, error) {
	var u AdminUser
	var expires string
	err := db.QueryRowContext(ctx, `SELECT u.id,u.username,u.status,u.created_at,u.last_login_at,u.origin,s.expires_at
		FROM admin_sessions s JOIN admin_users u ON u.id=s.user_id
		WHERE s.token_hash=? AND u.status='active'`, hash).
		Scan(&u.ID, &u.Username, &u.Status, &u.CreatedAt, &u.LastLoginAt, &u.Origin, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNotFound
	}
	if err != nil {
		return u, err
	}
	if expires <= now {
		if err := DeleteAdminSession(ctx, db, hash); err != nil {
			return u, err
		}
		return u, ErrNotFound
	}
	return u, nil
}

// DeleteAdminSession 重复注销幂等。
func DeleteAdminSession(ctx context.Context, db *sql.DB, hash string) error {
	_, err := db.ExecContext(ctx, `DELETE FROM admin_sessions WHERE token_hash=?`, hash)
	return err
}

// CleanAdminSessions 启动期清理，不注册后台巡检。
func CleanAdminSessions(ctx context.Context, db *sql.DB, now string) error {
	_, err := db.ExecContext(ctx, `DELETE FROM admin_sessions WHERE expires_at<=?`, now)
	return err
}

// GetAdminUserBySubject 按锦官权威 sub 读取联邦映射行。
func GetAdminUserBySubject(ctx context.Context, db *sql.DB, subject string) (AdminUser, error) {
	var u AdminUser
	err := db.QueryRowContext(ctx, `SELECT `+adminUserCols+`
		FROM admin_users WHERE external_subject=?`, subject).
		Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Status, &u.CreatedAt, &u.LastLoginAt,
			&u.Origin, &u.ExternalSubject)
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNotFound
	}
	return u, err
}

// ResolveFederatedAdmin 同一 sub 恒映射同一本地管理员:首次登录建号,用户名与既有
// 账号冲突时以 sub 前缀消歧。并发首登由唯一索引兜底,冲突后回查。
func ResolveFederatedAdmin(ctx context.Context, db *sql.DB, id, subject, username, now string) (AdminUser, error) {
	if u, err := GetAdminUserBySubject(ctx, db, subject); err == nil {
		return u, nil
	} else if !errors.Is(err, ErrNotFound) {
		return u, err
	}
	for attempt := 0; attempt < 2; attempt++ {
		name := username
		if attempt == 1 {
			name = username + "#" + subjectSuffix(subject)
		}
		// 联邦账号不落口令哈希:空哈希使本地账密轨对其恒失败(break-glass 仅限本地账号)
		if _, err := db.ExecContext(ctx, `INSERT INTO admin_users(id,username,password_hash,status,`+
			`created_at,origin,external_subject) VALUES(?,?,?,'active',?,'federated',?) ON CONFLICT DO NOTHING`,
			id, name, "", now, subject); err != nil {
			return AdminUser{}, err
		}
		u, err := GetAdminUserBySubject(ctx, db, subject)
		if err == nil {
			return u, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return u, err
		}
	}
	return AdminUser{}, MarkBusiness(fmt.Errorf("联邦管理员建号失败:sub 映射冲突"))
}

// subjectSuffix 取 sub 末端 8 字符作用户名消歧后缀;sub 为不可信输入,只保留字母数字。
func subjectSuffix(subject string) string {
	var sb strings.Builder
	for i := len(subject) - 1; i >= 0 && sb.Len() < 8; i-- {
		if isAlphaNum(subject[i]) {
			sb.WriteByte(subject[i])
		}
	}
	s := sb.String()
	if s == "" {
		return "federated"
	}
	return reverseBytes(s)
}

func isAlphaNum(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func reverseBytes(s string) string {
	b := []byte(s)
	for i, j := 0, len(b)-1; i < j; i, j = i+1, j-1 {
		b[i], b[j] = b[j], b[i]
	}
	return string(b)
}
