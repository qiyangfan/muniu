package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// AdminPrincipal 管理面主体:虎符 sub 即唯一键,不再有本地账密与影子 id。
// Username 只作展示与审计归因,不参与身份判定(同名主体可并存)。
type AdminPrincipal struct {
	Subject     string  `json:"subject"`
	Username    string  `json:"username"`
	Status      string  `json:"status"`
	CreatedAt   string  `json:"created_at"`
	LastLoginAt *string `json:"last_login_at"`
}

const adminPrincipalCols = `subject,username,status,created_at,last_login_at`

func scanAdminPrincipal(row interface{ Scan(...any) error }) (AdminPrincipal, error) {
	var p AdminPrincipal
	err := row.Scan(&p.Subject, &p.Username, &p.Status, &p.CreatedAt, &p.LastLoginAt)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}

// EnsureNoLocalAdminAccounts 启动闸:0012 时代的 admin_users 若含本地账密行,主体迁移会丢行,
// 必须显式拒绝启动并指名行,不允许静默丢弃。生产从未建过本地管理员,命中即需人工裁决。
func EnsureNoLocalAdminAccounts(ctx context.Context, db *sql.DB) error {
	var probed int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master
		WHERE type='table' AND name='admin_users'`).Scan(&probed); err != nil {
		return fmt.Errorf("store: 探测 admin_users: %w", err)
	}
	if probed == 0 {
		return nil
	}
	rows, err := db.QueryContext(ctx, `SELECT username FROM admin_users
		WHERE coalesce(origin,'local')<>'federated' ORDER BY username`)
	if err != nil {
		return fmt.Errorf("store: 探测本地管理员行: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return fmt.Errorf("store: 扫本地管理员行: %w", err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(names) > 0 {
		return MarkBusiness(fmt.Errorf(
			"store: admin_users 存在 %d 个本地账密管理员(%s),本地登录已移除,主体以虎符 sub 为准;"+
				"请确认后手工清理这些行再启动", len(names), strings.Join(names, ",")))
	}
	return nil
}

// ResolveAdminPrincipal 按虎符 sub 建/取主体:首次登录建号(active),用户名无唯一约束,
// 同名主体并存不影响身份。并发首登由主键兜底,冲突后回查。
func ResolveAdminPrincipal(ctx context.Context, db *sql.DB, subject, username, now string) (AdminPrincipal, error) {
	if _, err := db.ExecContext(ctx, `INSERT INTO admin_principals(subject,username,status,created_at)
		VALUES(?,?,'active',?) ON CONFLICT(subject) DO NOTHING`, subject, username, now); err != nil {
		return AdminPrincipal{}, err
	}
	return GetAdminPrincipal(ctx, db, subject)
}

// GetAdminPrincipal 按 sub 读取主体;不存在归 ErrNotFound。
func GetAdminPrincipal(ctx context.Context, db *sql.DB, subject string) (AdminPrincipal, error) {
	return scanAdminPrincipal(db.QueryRowContext(ctx,
		`SELECT `+adminPrincipalCols+` FROM admin_principals WHERE subject=?`, subject))
}

// ListAdminPrincipals 主体列表,按用户名升序便于控制台阅读。
func ListAdminPrincipals(ctx context.Context, db *sql.DB) ([]AdminPrincipal, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT `+adminPrincipalCols+` FROM admin_principals ORDER BY username,subject`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []AdminPrincipal{}
	for rows.Next() {
		p, err := scanAdminPrincipal(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// HasActiveAdminPrincipals 装配期判断是否因主体存在而挂载管理面。
func HasActiveAdminPrincipals(ctx context.Context, db *sql.DB) (bool, error) {
	var n int
	err := db.QueryRowContext(ctx, `SELECT count(*) FROM admin_principals WHERE status='active'`).Scan(&n)
	return n > 0, err
}

// PatchAdminPrincipal 启停主体;停用与删除存量会话同事务,无「停用后旧会话仍可用」窗口。
func PatchAdminPrincipal(ctx context.Context, db *sql.DB, subject string, disabled bool) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	status := "active"
	if disabled {
		status = "disabled"
	}
	res, err := tx.ExecContext(ctx, `UPDATE admin_principals SET status=? WHERE subject=?`, status, subject)
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
	if disabled {
		if _, err := tx.ExecContext(ctx, `DELETE FROM admin_sessions WHERE subject=?`, subject); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// CreateAdminSession 条件插入:签发与登录之间主体被停用时拒绝,不签发过时凭据。
func CreateAdminSession(ctx context.Context, db *sql.DB, subject, hash, created, expires string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `INSERT INTO admin_sessions(token_hash,subject,created_at,expires_at)
		SELECT ?,subject,?,? FROM admin_principals WHERE subject=? AND status='active'`,
		hash, created, expires, subject)
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
	if _, err := tx.ExecContext(ctx, `UPDATE admin_principals SET last_login_at=? WHERE subject=?`,
		created, subject); err != nil {
		return err
	}
	return tx.Commit()
}

// AdminSession 逐请求 JOIN 主体状态:停用后存量会话下一请求即失效;过期行惰性清理。
func AdminSession(ctx context.Context, db *sql.DB, hash, now string) (AdminPrincipal, error) {
	var p AdminPrincipal
	var expires string
	err := db.QueryRowContext(ctx, `SELECT p.subject,p.username,p.status,p.created_at,p.last_login_at,s.expires_at
		FROM admin_sessions s JOIN admin_principals p ON p.subject=s.subject
		WHERE s.token_hash=? AND p.status='active'`, hash).
		Scan(&p.Subject, &p.Username, &p.Status, &p.CreatedAt, &p.LastLoginAt, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	if err != nil {
		return p, err
	}
	if expires <= now {
		if err := DeleteAdminSession(ctx, db, hash); err != nil {
			return p, err
		}
		return p, ErrNotFound
	}
	return p, nil
}

// DeleteAdminSession 注销;重复注销幂等。
func DeleteAdminSession(ctx context.Context, db *sql.DB, hash string) error {
	_, err := db.ExecContext(ctx, `DELETE FROM admin_sessions WHERE token_hash=?`, hash)
	return err
}

// CleanAdminSessions 启动期清理,不注册后台巡检。
func CleanAdminSessions(ctx context.Context, db *sql.DB, now string) error {
	_, err := db.ExecContext(ctx, `DELETE FROM admin_sessions WHERE expires_at<=?`, now)
	return err
}
