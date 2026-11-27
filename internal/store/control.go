// 控制面消费侧的本地台账：快照 last-known 与运行时命令执行记录。
// 快照与投影价目/租户在同一事务内落库，保证「应用失败保留旧目录」的原子语义。
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// ControlSnapshotRecord 最近一次成功应用的控制快照。
type ControlSnapshotRecord struct {
	ServiceID  string
	Version    int64
	ETag       string
	ObservedAt string
	Signature  string
	Payload    []byte
	UpdatedAt  string
}

// LoadControlSnapshot 读取 last-known；无记录返回 ErrNotFound。
func LoadControlSnapshot(ctx context.Context, db *sql.DB, serviceID string) (*ControlSnapshotRecord, error) {
	var r ControlSnapshotRecord
	err := db.QueryRowContext(ctx, `SELECT service_id, version, etag, observed_at, signature, payload, updated_at
		FROM control_snapshots WHERE service_id = ?`, serviceID).
		Scan(&r.ServiceID, &r.Version, &r.ETag, &r.ObservedAt, &r.Signature, &r.Payload, &r.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: 读控制快照: %w", err)
	}
	return &r, nil
}

// ControlTenantRecord 快照里的租户投影字段；name 参与唯一约束，冲突即整包拒绝。
type ControlTenantRecord struct {
	ID                string
	Name              string
	Status            string
	DailyQuotaCredits int64
	TPMLimit          int64
}

// ControlState 一次控制快照应用需要原子写入的本地投影。
type ControlState struct {
	Snapshot            ControlSnapshotRecord
	ReplacePricing      bool
	Pricing             []PricingRecord
	ReplaceImagePricing bool
	ImagePricing        []ImagePricingRecord
	Tenants             []ControlTenantRecord
	Audit               *AdminAudit
}

// ApplyControlState 单事务应用快照投影：价目整表替换、租户 upsert 且缺席者停用、快照 upsert、审计。
// 任何一步失败整包回滚，调用方保留旧运行目录。
func ApplyControlState(ctx context.Context, db *sql.DB, st ControlState) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: 开控制快照事务: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := applyPriceProjections(ctx, tx, st); err != nil {
		return err
	}
	if err := replaceTenantsTx(ctx, tx, st.Tenants, st.Snapshot.UpdatedAt); err != nil {
		return err
	}
	if err := upsertSnapshotTx(ctx, tx, st.Snapshot); err != nil {
		return err
	}
	if st.Audit != nil {
		if err := insertAuditTx(ctx, tx, *st.Audit); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: 提交控制快照事务: %w", err)
	}
	return nil
}

// applyPriceProjections token 与图片价目按各自的替换开关整表投影。
func applyPriceProjections(ctx context.Context, tx *sql.Tx, st ControlState) error {
	if st.ReplacePricing {
		if err := replacePricingTx(ctx, tx, st.Pricing); err != nil {
			return err
		}
	}
	if st.ReplaceImagePricing {
		if err := replaceImagePricingTx(ctx, tx, st.ImagePricing); err != nil {
			return err
		}
	}
	return nil
}

// replacePricingTx 运行态价目整表替换为快照投影。
func replacePricingTx(ctx context.Context, tx *sql.Tx, rows []PricingRecord) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM model_pricing`); err != nil {
		return fmt.Errorf("store: 清空控制面价目: %w", err)
	}
	for _, r := range rows {
		windows, err := json.Marshal(r.Windows)
		if err != nil {
			return fmt.Errorf("store: 序列化价目窗口 %s: %w", r.Alias, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO model_pricing
			(alias, currency, input_credits, cached_input_credits, vision_input_credits,
			 output_credits, timezone, time_windows, updated_at)
			VALUES (?,?,?,?,?,?,?,?,?)`,
			r.Alias, r.Currency, r.InputCredits, r.CachedInputCredits, r.VisionInputCredits,
			r.OutputCredits, r.Timezone, string(windows), r.UpdatedAt); err != nil {
			return fmt.Errorf("store: 写控制面价目 %s: %w", r.Alias, err)
		}
	}
	return nil
}

// replaceImagePricingTx 运行态图片价目整表替换为快照投影。
func replaceImagePricingTx(ctx context.Context, tx *sql.Tx, rows []ImagePricingRecord) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM image_pricing`); err != nil {
		return fmt.Errorf("store: 清空控制面图片价目: %w", err)
	}
	for _, r := range rows {
		tiers := r.Tiers
		if tiers == nil {
			tiers = map[string]int64{}
		}
		raw, err := json.Marshal(tiers)
		if err != nil {
			return fmt.Errorf("store: 序列化图片档位 %s: %w", r.Alias, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO image_pricing
			(alias, currency, default_credits, tiers, updated_at)
			VALUES (?,?,?,?,?)`,
			r.Alias, r.Currency, r.DefaultCredits, string(raw), r.UpdatedAt); err != nil {
			return fmt.Errorf("store: 写控制面图片价目 %s: %w", r.Alias, err)
		}
	}
	return nil
}

// replaceTenantsTx 租户投影 upsert；快照缺席的本地租户停用而非删除（key 外键仍可读历史）。
// 同名不同 ID 的本地残留先对账：无引用则删除，有引用则显式冲突拒绝，禁止无限重试锁死。
func replaceTenantsTx(ctx context.Context, tx *sql.Tx, rows []ControlTenantRecord, now string) error {
	ids := make([]any, 0, len(rows))
	for _, r := range rows {
		if err := reconcileTenantConflict(ctx, tx, r); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO tenants
			(id, name, status, created_at, daily_quota_credits, tpm_limit)
			VALUES (?,?,?,?,?,?)
			ON CONFLICT(id) DO UPDATE SET
			  name=excluded.name, status=excluded.status,
			  daily_quota_credits=excluded.daily_quota_credits, tpm_limit=excluded.tpm_limit`,
			r.ID, r.Name, r.Status, now, r.DailyQuotaCredits, r.TPMLimit); err != nil {
			return fmt.Errorf("store: 写控制面租户 %s: %w", r.ID, err)
		}
		ids = append(ids, r.ID)
	}
	query := `UPDATE tenants SET status='disabled'`
	args := []any(nil)
	if len(ids) > 0 {
		query += ` WHERE id NOT IN (` + controlPlaceholders(len(ids)) + `)` //nolint:gosec // 仅拼接 ? 占位符,值全部参数绑定
		args = ids
	}
	if _, err := tx.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("store: 停用快照缺席租户: %w", err)
	}
	return nil
}

// reconcileTenantConflict 处理本地同名不同 ID 的租户占用：无引用可安全删除，
// 有引用说明本地历史归属无法静默改写，整包拒绝并给出对账明细。
func reconcileTenantConflict(ctx context.Context, tx *sql.Tx, r ControlTenantRecord) error {
	var localID string
	err := tx.QueryRowContext(ctx, `SELECT id FROM tenants WHERE name = ? AND id <> ?`,
		r.Name, r.ID).Scan(&localID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("store: 查租户同名冲突: %w", err)
	}
	refs, err := tenantRefCounts(ctx, tx, localID)
	if err != nil {
		return err
	}
	if refs.apiKeys > 0 || refs.semanticCache > 0 || refs.principalBindings > 0 {
		return fmt.Errorf("store: tenant_identity_conflict 租户身份冲突 name=%s local_id=%s control_id=%s "+
			"引用 api_keys=%d semantic_cache=%d principal_bindings=%d",
			r.Name, localID, r.ID, refs.apiKeys, refs.semanticCache, refs.principalBindings)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM tenants WHERE id = ?`, localID); err != nil {
		return fmt.Errorf("store: 删除同名租户残留 %s: %w", localID, err)
	}
	return nil
}

// tenantRefs 本地租户被引用计数，删除前必须为零。
type tenantRefs struct {
	apiKeys           int64
	semanticCache     int64
	principalBindings int64
}

func tenantRefCounts(ctx context.Context, tx *sql.Tx, tenantID string) (tenantRefs, error) {
	var r tenantRefs
	err := tx.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM api_keys WHERE tenant_id = ?),
		(SELECT COUNT(*) FROM semantic_cache WHERE tenant_id = ?),
		(SELECT COUNT(*) FROM admin_principal_tenants WHERE tenant_id = ?)`,
		tenantID, tenantID, tenantID).
		Scan(&r.apiKeys, &r.semanticCache, &r.principalBindings)
	if err != nil {
		return tenantRefs{}, fmt.Errorf("store: 统计租户引用 %s: %w", tenantID, err)
	}
	return r, nil
}

// upsertSnapshotTx 快照落库；版本单调，旧版本不得覆盖新版本。
func upsertSnapshotTx(ctx context.Context, tx *sql.Tx, r ControlSnapshotRecord) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO control_snapshots
		(service_id, version, etag, observed_at, signature, payload, updated_at)
		VALUES (?,?,?,?,?,?,?)
		ON CONFLICT(service_id) DO UPDATE SET
		  version=excluded.version, etag=excluded.etag, observed_at=excluded.observed_at,
		  signature=excluded.signature, payload=excluded.payload, updated_at=excluded.updated_at
		WHERE excluded.version >= control_snapshots.version`,
		r.ServiceID, r.Version, r.ETag, r.ObservedAt, r.Signature, string(r.Payload), r.UpdatedAt); err != nil {
		return fmt.Errorf("store: 写控制快照: %w", err)
	}
	return nil
}

// insertAuditTx 系统审计与快照写入同事务：审计失败则快照应用一并回滚。
func insertAuditTx(ctx context.Context, tx *sql.Tx, a AdminAudit) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO admin_audit_log VALUES(?,?,?,?,?,?,?,?,?)`,
		a.ID, a.RequestID, a.ActorKind, a.ActorName, a.Action, a.TargetID, a.Outcome, a.Detail,
		a.CreatedAt); err != nil {
		return fmt.Errorf("store: 写控制快照审计: %w", err)
	}
	return nil
}

// controlPlaceholders 生成 n 个逗号分隔的 ? 占位符。
func controlPlaceholders(n int) string {
	if n <= 0 {
		return ""
	}
	out := "?"
	for i := 1; i < n; i++ {
		out += ",?"
	}
	return out
}

// ControlCommandRow 运行时命令台账行；control 来源保留锦官幂等键，local 来源不排队。
type ControlCommandRow struct {
	ID                  string
	Action              string
	ActionSchemaVersion int64
	Params              string
	Source              string
	Status              string
	Result              string
	Attempts            int64
	IdempotencyKey      string
	Actor               string
	Reason              string
	CreatedAt           string
	CompletedAt         string
}

// InsertControlCommand 幂等插入；已存在返回 inserted=false。
func InsertControlCommand(ctx context.Context, db *sql.DB, r ControlCommandRow) (bool, error) {
	res, err := db.ExecContext(ctx, `INSERT OR IGNORE INTO control_commands
		(id, action, action_schema_version, params, source, status, result, attempts,
		 idempotency_key, actor, reason, created_at, completed_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.ID, r.Action, r.ActionSchemaVersion, r.Params, r.Source, r.Status, r.Result, r.Attempts,
		r.IdempotencyKey, r.Actor, r.Reason, r.CreatedAt, nullIfEmpty(r.CompletedAt))
	if err != nil {
		return false, fmt.Errorf("store: 写运行时命令: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// GetControlCommand 按 id 读台账，不存在返回 ErrNotFound。
func GetControlCommand(ctx context.Context, db *sql.DB, id string) (*ControlCommandRow, error) {
	var r ControlCommandRow
	var completed sql.NullString
	err := db.QueryRowContext(ctx, `SELECT id, action, action_schema_version, params, source, status,
		result, attempts, idempotency_key, actor, reason, created_at, completed_at
		FROM control_commands WHERE id = ?`, id).
		Scan(&r.ID, &r.Action, &r.ActionSchemaVersion, &r.Params, &r.Source, &r.Status,
			&r.Result, &r.Attempts, &r.IdempotencyKey, &r.Actor, &r.Reason, &r.CreatedAt, &completed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: 读运行时命令: %w", err)
	}
	r.CompletedAt = completed.String
	return &r, nil
}

// UpdateControlCommandResult 落终态并返回影响行数。
func UpdateControlCommandResult(ctx context.Context, db *sql.DB, id, status, result, completedAt string) error {
	_, err := db.ExecContext(ctx, `UPDATE control_commands SET status=?, result=?, completed_at=?
		WHERE id=?`, status, result, completedAt, id)
	if err != nil {
		return fmt.Errorf("store: 更新运行时命令: %w", err)
	}
	return nil
}

// BumpControlCommandAttempts 累加重试次数并返回新值。
func BumpControlCommandAttempts(ctx context.Context, db *sql.DB, id string) (int64, error) {
	if _, err := db.ExecContext(ctx, `UPDATE control_commands SET attempts=attempts+1 WHERE id=?`, id); err != nil {
		return 0, fmt.Errorf("store: 累加运行时命令重试: %w", err)
	}
	var n int64
	if err := db.QueryRowContext(ctx, `SELECT attempts FROM control_commands WHERE id=?`, id).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: 读运行时命令重试次数: %w", err)
	}
	return n, nil
}

// ListControlCommands 按创建倒序取最近命令。
func ListControlCommands(ctx context.Context, db *sql.DB, limit int) ([]ControlCommandRow, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := db.QueryContext(ctx, `SELECT id, action, action_schema_version, params, source, status,
		result, attempts, idempotency_key, actor, reason, created_at, completed_at
		FROM control_commands ORDER BY created_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("store: 列运行时命令: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []ControlCommandRow{}
	for rows.Next() {
		var r ControlCommandRow
		var completed sql.NullString
		if err := rows.Scan(&r.ID, &r.Action, &r.ActionSchemaVersion, &r.Params, &r.Source, &r.Status,
			&r.Result, &r.Attempts, &r.IdempotencyKey, &r.Actor, &r.Reason, &r.CreatedAt, &completed); err != nil {
			return nil, fmt.Errorf("store: 扫运行时命令: %w", err)
		}
		r.CompletedAt = completed.String
		out = append(out, r)
	}
	return out, rows.Err()
}

// CountControlCommandsByStatus 台账状态计数；错误返回空表。
func CountControlCommandsByStatus(ctx context.Context, db *sql.DB) (map[string]int64, error) {
	rows, err := db.QueryContext(ctx, `SELECT status, COUNT(*) FROM control_commands GROUP BY status`)
	if err != nil {
		return map[string]int64{}, fmt.Errorf("store: 统计运行时命令: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]int64{"pending": 0, "acked": 0, "failed": 0}
	for rows.Next() {
		var status string
		var n int64
		if err := rows.Scan(&status, &n); err != nil {
			return map[string]int64{}, fmt.Errorf("store: 扫运行时命令统计: %w", err)
		}
		out[status] = n
	}
	return out, rows.Err()
}
