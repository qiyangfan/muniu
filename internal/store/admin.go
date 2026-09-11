// 管理 API 读侧 repo(T4.1):租户 CRUD、usage 多维聚合。
// 依赖方向:store 只被 admin/auth/metering/tenant 消费,只出不进。
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Tenant 租户行(管理侧形态;JSON 字段 snake_case 供管理 API 输出)。
type Tenant struct {
	ID                  string `json:"id"`
	Name                string `json:"name"`
	Status              string `json:"status"`
	DailyQuotaMicro     int64  `json:"daily_quota_micro"`
	TPMLimit            int64  `json:"tpm_limit"`
	SemanticCacheEnable bool   `json:"semantic_cache_enabled"`
	CreatedAt           string `json:"created_at"`
}

// ListTenants 租户列表(创建时间升序)。
func ListTenants(db *sql.DB) ([]Tenant, error) {
	rows, err := db.Query(`SELECT id, name, status, daily_quota_micro, tpm_limit,
		semantic_cache_enabled, created_at FROM tenants ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("store: 列出租户: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Tenant
	for rows.Next() {
		var t Tenant
		var en int64
		if err := rows.Scan(&t.ID, &t.Name, &t.Status, &t.DailyQuotaMicro, &t.TPMLimit, &en, &t.CreatedAt); err != nil {
			return nil, fmt.Errorf("store: 扫租户行: %w", err)
		}
		t.SemanticCacheEnable = en == 1
		out = append(out, t)
	}
	return out, rows.Err()
}

// CreateTenant 创建租户(名称唯一)。
func CreateTenant(db *sql.DB, name string) (string, error) {
	id := uuid.Must(uuid.NewV7()).String()
	_, err := db.Exec(`INSERT INTO tenants (id, name, created_at) VALUES (?,?,?)`,
		id, name, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return "", MarkBusiness(fmt.Errorf("store: 租户名 %q 已存在", name))
		}
		return "", fmt.Errorf("store: 创建租户: %w", err)
	}
	return id, nil
}

// UpdateTenantPatch 可更新字段(零值=不动)。
type UpdateTenantPatch struct {
	Status              string // active | disabled
	DailyQuotaMicro     *int64
	TPMLimit            *int64
	SemanticCacheEnable *bool
}

// UpdateTenant 按 id 更新租户;返回是否命中行。
func UpdateTenant(db *sql.DB, id string, p UpdateTenantPatch) (bool, error) {
	sets, args := []string{}, []any{}
	if p.Status != "" {
		if p.Status != "active" && p.Status != "disabled" {
			return false, MarkBusiness(fmt.Errorf("store: status 非法 %q", p.Status))
		}
		sets = append(sets, "status = ?")
		args = append(args, p.Status)
	}
	if p.DailyQuotaMicro != nil {
		sets = append(sets, "daily_quota_micro = ?")
		args = append(args, *p.DailyQuotaMicro)
	}
	if p.TPMLimit != nil {
		sets = append(sets, "tpm_limit = ?")
		args = append(args, *p.TPMLimit)
	}
	if p.SemanticCacheEnable != nil {
		v := int64(0)
		if *p.SemanticCacheEnable {
			v = 1
		}
		sets = append(sets, "semantic_cache_enabled = ?")
		args = append(args, v)
	}
	if len(sets) == 0 {
		return false, nil
	}
	args = append(args, id)
	res, err := db.Exec(`UPDATE tenants SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...)
	if err != nil {
		return false, fmt.Errorf("store: 更新租户: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// UsageDim 聚合维度。
type UsageDim string

// businessError 业务类错误标记(校验/冲突/不存在等可对管理员原样呈现的错误)。
// 消息文本保持原样,仅供 admin 层区分「400+可读」与「500+内部错误」(conventions 错误纪律)。
type businessError struct{ err error }

func (b businessError) Error() string { return b.err.Error() }
func (b businessError) Unwrap() error { return b.err }

// MarkBusiness 将错误标记为业务类(消息不变;导出供 auth 等同层包标记)。
func MarkBusiness(err error) error { return businessError{err} }

// IsBusiness 判定是否业务类错误。
func IsBusiness(err error) bool {
	var b businessError
	return errors.As(err, &b)
}

// 聚合维度常量。
const (
	DimDay      UsageDim = "day"
	DimKey      UsageDim = "key"
	DimModel    UsageDim = "model"
	DimProvider UsageDim = "provider"
)

// UsagePoint 一个聚合桶:维度值 + token/成本/次数(json tag 为管理 API 对外契约,
// 缺失会以 Go 字段名输出 PascalCase,控制台/脚本全部读不到)。
type UsagePoint struct {
	Dim              string `json:"dim"` // 天(YYYY-MM-DD)/ api_key_id / model_alias / provider
	Requests         int64  `json:"requests"`
	PromptTokens     int64  `json:"prompt_tokens"`
	CompletionTokens int64  `json:"completion_tokens"`
	CacheHits        int64  `json:"cache_hits"` // provider='semantic-cache' 的行数
	CostMicro        int64  `json:"cost_micro"`
}

// UsageAggregate 按维度与时间范围聚合 usage_logs(管理 API 用)。
// from/to 为 RFC3339 UTC;estimated 行不参与成本口径(metering 契约),但计入请求数。
func UsageAggregate(db *sql.DB, dim UsageDim, from, to, tenantID, keyID string) ([]UsagePoint, error) {
	var expr string
	switch dim {
	case DimDay:
		expr = `substr(created_at,1,10)`
	case DimKey:
		expr = `api_key_id`
	case DimModel:
		expr = `model_alias`
	case DimProvider:
		expr = `provider`
	default:
		return nil, MarkBusiness(fmt.Errorf("store: 未知聚合维度 %q", dim))
	}
	q := `SELECT ` + expr + ` AS dim, COUNT(*) AS reqs,
			COALESCE(SUM(prompt_tokens),0), COALESCE(SUM(completion_tokens),0),
			COALESCE(SUM(CASE WHEN provider='semantic-cache' THEN 1 ELSE 0 END),0),
			COALESCE(SUM(CASE WHEN estimated=0 THEN cost_micro ELSE 0 END),0)
		FROM usage_logs WHERE created_at >= ? AND created_at <= ?`
	args := []any{from, to}
	if tenantID != "" {
		q += ` AND tenant_id = ?`
		args = append(args, tenantID)
	}
	if keyID != "" {
		q += ` AND api_key_id = ?`
		args = append(args, keyID)
	}
	q += ` GROUP BY dim ORDER BY dim`
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: usage 聚合: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []UsagePoint
	for rows.Next() {
		var p UsagePoint
		if err := rows.Scan(&p.Dim, &p.Requests, &p.PromptTokens, &p.CompletionTokens, &p.CacheHits, &p.CostMicro); err != nil {
			return nil, fmt.Errorf("store: 扫聚合行: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
