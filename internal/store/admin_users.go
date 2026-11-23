package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// AdminPrincipal 管理面主体:Keycloak sub 即唯一键,不再有本地账密与影子 id。
// Username 只作展示与审计归因,不参与身份判定(同名主体可并存)。
// Scope 是显式范围枚举;TenantIDs 仅在 ListAdminPrincipals 填充,
// 单主体读取需另调 AdminPrincipalTenants(避免登录热路径隐式 N+1)。
type AdminPrincipal struct {
	Subject     string   `json:"subject"`
	Username    string   `json:"username"`
	Status      string   `json:"status"`
	Scope       string   `json:"scope"`
	TenantIDs   []string `json:"tenant_ids"`
	CreatedAt   string   `json:"created_at"`
	LastLoginAt *string  `json:"last_login_at"`
}

const adminPrincipalCols = `subject,username,status,scope,created_at,last_login_at`

func scanAdminPrincipal(row interface{ Scan(...any) error }) (AdminPrincipal, error) {
	var p AdminPrincipal
	p.TenantIDs = []string{}
	err := row.Scan(&p.Subject, &p.Username, &p.Status, &p.Scope, &p.CreatedAt, &p.LastLoginAt)
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
			"store: admin_users 存在 %d 个本地账密管理员(%s),本地登录已移除,主体以Keycloak sub 为准;"+
				"请确认后手工清理这些行再启动", len(names), strings.Join(names, ",")))
	}
	return nil
}

// ResolveAdminPrincipal 按Keycloak sub 建/取主体:首次登录建号(active),用户名无唯一约束,
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

// ListAdminPrincipals 主体列表,按用户名升序便于控制台阅读;绑定一次读全表分组,
// 管理面主体基数极小,不逐主体查询。
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
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := attachTenantBindings(ctx, db, out); err != nil {
		return nil, err
	}
	return out, nil
}

// attachTenantBindings 一次读全绑定表并按主体分组,避免逐主体查询。
func attachTenantBindings(ctx context.Context, db *sql.DB, principals []AdminPrincipal) error {
	bindings, err := db.QueryContext(ctx,
		`SELECT subject,tenant_id FROM admin_principal_tenants ORDER BY subject,tenant_id`)
	if err != nil {
		return err
	}
	defer func() { _ = bindings.Close() }()
	bySubject := map[string][]string{}
	for bindings.Next() {
		var subject, tenantID string
		if err := bindings.Scan(&subject, &tenantID); err != nil {
			return err
		}
		bySubject[subject] = append(bySubject[subject], tenantID)
	}
	if err := bindings.Err(); err != nil {
		return err
	}
	for i := range principals {
		if ids, ok := bySubject[principals[i].Subject]; ok {
			principals[i].TenantIDs = ids
		}
	}
	return nil
}

// patchExecutor 事务与连接共用的查询面:BEGIN IMMEDIATE 事务跑在 *sql.Conn 上,
// 校验与写回 helper 据此复用,sql.Tx 与 sql.Conn 都可传入。
type patchExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// AdminPrincipalTenants 单主体绑定租户,租户 id 升序;空集合返回空切片而非 nil。
func AdminPrincipalTenants(ctx context.Context, db *sql.DB, subject string) ([]string, error) {
	return adminPrincipalTenants(ctx, db, subject)
}

func adminPrincipalTenants(ctx context.Context, db patchExecutor, subject string) ([]string, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT tenant_id FROM admin_principal_tenants WHERE subject=? ORDER BY tenant_id`, subject)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []string{}
	for rows.Next() {
		var tenantID string
		if err := rows.Scan(&tenantID); err != nil {
			return nil, err
		}
		out = append(out, tenantID)
	}
	return out, rows.Err()
}

// AdminPrincipalPatch 管理主体补丁:字段为 nil 表示不动;TenantIDs 非 nil 时整包替换。
type AdminPrincipalPatch struct {
	Disabled  *bool
	Scope     *string
	TenantIDs *[]string
}

// SetAdminPrincipalScope 整包替换主体的范围与绑定。主体不存在返回 ErrNotFound。
func SetAdminPrincipalScope(ctx context.Context, db *sql.DB, subject, scope string, tenantIDs []string) error {
	return PatchAdminPrincipalFields(ctx, db, subject,
		AdminPrincipalPatch{Scope: &scope, TenantIDs: &tenantIDs})
}

// PatchAdminPrincipalFields 单事务应用主体补丁:范围/绑定/停用一次落库,任一步校验失败整单回滚。
// 事务以 BEGIN IMMEDIATE 先取写锁:deferred 事务里的「读当前值→合并→写回」在并发补丁间
// 会双双成功却互相覆盖(一方意图静默丢失),控制面写路径不允许这种假成功。
func PatchAdminPrincipalFields(ctx context.Context, db *sql.DB, subject string, p AdminPrincipalPatch) error {
	return withImmediateTx(ctx, db, func(conn *sql.Conn) error {
		var curScope string
		if err := conn.QueryRowContext(ctx,
			`SELECT scope FROM admin_principals WHERE subject=?`, subject).Scan(&curScope); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if p.Scope != nil || p.TenantIDs != nil {
			if err := patchScopeAndBindings(ctx, conn, subject, curScope, p); err != nil {
				return err
			}
		}
		if p.Disabled != nil {
			return patchDisabled(ctx, conn, subject, *p.Disabled)
		}
		return nil
	})
}

// withImmediateTx 在专用连接上以 BEGIN IMMEDIATE 执行写事务:deferred 事务里的
// 「读当前值→合并→写回」在并发补丁间会双双成功却互相覆盖(一方意图静默丢失),
// 写锁从语句开始即持有才能让后到者读到前者的提交结果。
func withImmediateTx(ctx context.Context, db *sql.DB, fn func(*sql.Conn) error) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.WithoutCancel(ctx), `ROLLBACK`)
		}
	}()
	if err := fn(conn); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return err
	}
	committed = true
	return nil
}

// patchScopeAndBindings 合并省略字段后校验并写回范围与绑定(调用方事务内)。
func patchScopeAndBindings(ctx context.Context, db patchExecutor, subject, curScope string,
	p AdminPrincipalPatch) error {
	nextScope := curScope
	if p.Scope != nil {
		nextScope = *p.Scope
	}
	var nextTenants []string
	if p.TenantIDs != nil {
		nextTenants = dedupeTenantIDs(*p.TenantIDs)
	} else {
		var err error
		nextTenants, err = adminPrincipalTenants(ctx, db, subject)
		if err != nil {
			return err
		}
	}
	if err := validateScope(nextScope, nextTenants); err != nil {
		return err
	}
	if err := ensureTenantsExist(ctx, db, nextTenants); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx,
		`UPDATE admin_principals SET scope=? WHERE subject=?`, nextScope, subject); err != nil {
		return err
	}
	return replaceTenantBindings(ctx, db, subject, nextTenants)
}

// patchDisabled 写主体状态;停用即删该主体全部会话(调用方事务内)。
func patchDisabled(ctx context.Context, db patchExecutor, subject string, disabled bool) error {
	status := "active"
	if disabled {
		status = "disabled"
	}
	if _, err := db.ExecContext(ctx,
		`UPDATE admin_principals SET status=? WHERE subject=?`, status, subject); err != nil {
		return err
	}
	if !disabled {
		return nil
	}
	_, err := db.ExecContext(ctx, `DELETE FROM admin_sessions WHERE subject=?`, subject)
	return err
}

// validateScope 范围写入的输入契约:值域、GLOBAL 不得带绑定、租户 id 非空。
func validateScope(scope string, ids []string) error {
	if scope != "GLOBAL" && scope != "TENANT" {
		return MarkBusiness(fmt.Errorf("store: 未知 scope %q", scope))
	}
	if scope == "GLOBAL" && len(ids) > 0 {
		return MarkBusiness(errors.New("store: GLOBAL 范围不允许携带租户绑定"))
	}
	for _, id := range ids {
		if id == "" {
			return MarkBusiness(errors.New("store: 租户 id 不可为空"))
		}
	}
	return nil
}

// ensureTenantsExist 事务内校验全部绑定租户存在,未知租户整单拒绝。
func ensureTenantsExist(ctx context.Context, db patchExecutor, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	exist := map[string]bool{}
	//nolint:gosec // 占位符数量按切片长度生成,租户 id 全部经参数绑定
	rows, err := db.QueryContext(ctx,
		`SELECT id FROM tenants WHERE id IN (`+placeholders(len(ids))+`)`, anyArgs(ids)...)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		exist[id] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		if !exist[id] {
			return MarkBusiness(fmt.Errorf("store: 未知租户 %q", id))
		}
	}
	return nil
}

// replaceTenantBindings 清空旧绑定后写入新集合(整包替换)。
func replaceTenantBindings(ctx context.Context, db patchExecutor, subject string, ids []string) error {
	if _, err := db.ExecContext(ctx, `DELETE FROM admin_principal_tenants WHERE subject=?`, subject); err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := db.ExecContext(ctx,
			`INSERT INTO admin_principal_tenants(subject,tenant_id) VALUES(?,?)`, subject, id); err != nil {
			return err
		}
	}
	return nil
}

// dedupeTenantIDs 去重保序,重复输入不产生主键冲突。
func dedupeTenantIDs(ids []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// placeholders 生成 n 个逗号分隔的 ? 占位符。
func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// anyArgs 字符串切片转 []any,供变参查询。
func anyArgs(values []string) []any {
	out := make([]any, len(values))
	for i, v := range values {
		out[i] = v
	}
	return out
}

// HasActiveAdminPrincipals 装配期判断是否因主体存在而挂载管理面。
func HasActiveAdminPrincipals(ctx context.Context, db *sql.DB) (bool, error) {
	var n int
	err := db.QueryRowContext(ctx, `SELECT count(*) FROM admin_principals WHERE status='active'`).Scan(&n)
	return n > 0, err
}

// PatchAdminPrincipal 启停主体;停用与删除存量会话同事务,无「停用后旧会话仍可用」窗口。
func PatchAdminPrincipal(ctx context.Context, db *sql.DB, subject string, disabled bool) error {
	return PatchAdminPrincipalFields(ctx, db, subject, AdminPrincipalPatch{Disabled: &disabled})
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
	err := db.QueryRowContext(ctx, `SELECT p.subject,p.username,p.status,p.scope,p.created_at,p.last_login_at,s.expires_at
		FROM admin_sessions s JOIN admin_principals p ON p.subject=s.subject
		WHERE s.token_hash=? AND p.status='active'`, hash).
		Scan(&p.Subject, &p.Username, &p.Status, &p.Scope, &p.CreatedAt, &p.LastLoginAt, &expires)
	p.TenantIDs = []string{}
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
