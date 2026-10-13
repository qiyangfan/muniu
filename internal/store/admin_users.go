package store

import (
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
func CreateAdminUser(db *sql.DB, u AdminUser) error {
	_, err := db.Exec(`INSERT INTO admin_users(id,username,password_hash,created_at) VALUES(?,?,?,?)`, u.ID, u.Username, u.PasswordHash, u.CreatedAt)
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed") {
		return MarkBusiness(fmt.Errorf("管理员用户名已存在"))
	}
	return err
}

// GetAdminUser 按用户名读取，仅认证域消费哈希。
func GetAdminUser(db *sql.DB, username string) (AdminUser, error) {
	var u AdminUser
	err := db.QueryRow(`SELECT id,username,password_hash,status,created_at,last_login_at FROM admin_users WHERE username=?`, username).Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Status, &u.CreatedAt, &u.LastLoginAt)
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNotFound
	}
	return u, err
}

// ListAdminUsers 公开形态不读取哈希。
func ListAdminUsers(db *sql.DB) ([]AdminUser, error) {
	rows, err := db.Query(`SELECT id,username,status,created_at,last_login_at FROM admin_users ORDER BY username`)
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
func HasActiveAdmins(db *sql.DB) (bool, error) {
	var n int
	err := db.QueryRow(`SELECT count(*) FROM admin_users WHERE status='active'`).Scan(&n)
	return n > 0, err
}

// PatchAdminUser 账号变更与存量会话删除同事务，改密/停用无短暂旧会话窗口。
func PatchAdminUser(db *sql.DB, id string, disabled *bool, hash string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var old string
	if err := tx.QueryRow(`SELECT status FROM admin_users WHERE id=?`, id).Scan(&old); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if disabled != nil {
		status := "active"
		if *disabled {
			status = "disabled"
		}
		if _, err := tx.Exec(`UPDATE admin_users SET status=? WHERE id=?`, status, id); err != nil {
			return err
		}
	}
	if hash != "" {
		if _, err := tx.Exec(`UPDATE admin_users SET password_hash=? WHERE id=?`, hash, id); err != nil {
			return err
		}
	}
	if hash != "" || disabled != nil && *disabled {
		if _, err := tx.Exec(`DELETE FROM admin_sessions WHERE user_id=?`, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// CreateAdminSession 登录校验与签发之间若停用或改密，条件插入拒绝过时的凭据。
func CreateAdminSession(db *sql.DB, u AdminUser, hash, created, expires string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.Exec(`INSERT INTO admin_sessions(token_hash,user_id,created_at,expires_at) SELECT ?,id,?,? FROM admin_users WHERE id=? AND status='active' AND password_hash=?`, hash, created, expires, u.ID, u.PasswordHash)
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
	if _, err := tx.Exec(`UPDATE admin_users SET last_login_at=? WHERE id=?`, created, u.ID); err != nil {
		return err
	}
	return tx.Commit()
}

// AdminSession 每次查活跃账号，过期行惰性清理。
func AdminSession(db *sql.DB, hash, now string) (AdminUser, error) {
	var u AdminUser
	var expires string
	err := db.QueryRow(`SELECT u.id,u.username,u.status,u.created_at,u.last_login_at,s.expires_at FROM admin_sessions s JOIN admin_users u ON u.id=s.user_id WHERE s.token_hash=? AND u.status='active'`, hash).Scan(&u.ID, &u.Username, &u.Status, &u.CreatedAt, &u.LastLoginAt, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNotFound
	}
	if err != nil {
		return u, err
	}
	if expires <= now {
		if err := DeleteAdminSession(db, hash); err != nil {
			return u, err
		}
		return u, ErrNotFound
	}
	return u, nil
}

// DeleteAdminSession 重复注销幂等。
func DeleteAdminSession(db *sql.DB, hash string) error {
	_, err := db.Exec(`DELETE FROM admin_sessions WHERE token_hash=?`, hash)
	return err
}

// CleanAdminSessions 启动期清理，不注册后台巡检。
func CleanAdminSessions(db *sql.DB, now string) error {
	_, err := db.Exec(`DELETE FROM admin_sessions WHERE expires_at<=?`, now)
	return err
}
