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
	ID           string  `json:"id"`
	Username     string  `json:"username"`
	PasswordHash string  `json:"-"`
	Status       string  `json:"status"`
	CreatedAt    string  `json:"created_at"`
	LastLoginAt  *string `json:"last_login_at"`
}

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
	err := db.QueryRowContext(ctx, `SELECT id,username,password_hash,status,created_at,last_login_at
		FROM admin_users WHERE username=?`, username).
		Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Status, &u.CreatedAt, &u.LastLoginAt)
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNotFound
	}
	return u, err
}

// ListAdminUsers 公开形态不读取哈希。
func ListAdminUsers(ctx context.Context, db *sql.DB) ([]AdminUser, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id,username,status,created_at,last_login_at FROM admin_users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []AdminUser{}
	for rows.Next() {
		var u AdminUser
		if err := rows.Scan(&u.ID, &u.Username, &u.Status, &u.CreatedAt, &u.LastLoginAt); err != nil {
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
	err := db.QueryRowContext(ctx, `SELECT u.id,u.username,u.status,u.created_at,u.last_login_at,s.expires_at
		FROM admin_sessions s JOIN admin_users u ON u.id=s.user_id
		WHERE s.token_hash=? AND u.status='active'`, hash).
		Scan(&u.ID, &u.Username, &u.Status, &u.CreatedAt, &u.LastLoginAt, &expires)
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
