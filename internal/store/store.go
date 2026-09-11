// Package store 承载 SQLite 存取:打开(pragma)、embed 迁移执行与 repo 方法。
// 铁则(architecture「包结构」):store 只被 metering/auth/tenant 消费,依赖方向只出不进;
// 已应用的迁移文件禁改,修 schema 只追加新文件(data-and-config「迁移机制」)。
package store

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite" // 纯 Go SQLite 驱动(CGO_ENABLED=0 可用)
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Open 打开(必要时创建)SQLite 库并执行未应用迁移。
// pragma 随 DSN 传入:WAL + busy_timeout 5s + foreign_keys(data-and-config「迁移机制」)。
func Open(path string) (*sql.DB, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("store: 创建数据目录: %w", err)
		}
	}
	dsn := path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: 打开数据库: %w", err)
	}
	// 单文件库:连接复用同一句柄,规避多连接写锁竞争;读并发由 WAL 承担
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: 连接数据库: %w", err)
	}
	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// migrate 顺序执行未应用的迁移:单事务一文件,失败整体回滚并中止启动。
// 幂等:schema_migrations 已记录的版本跳过,重复打开/并发打开安全。
// 注意:账表由 0001 迁移按契约 DDL 创建(禁在此预建,否则与逐字 DDL 冲突),
// 执行器经 sqlite_master 探测其存在性。
func migrate(db *sql.DB) error {
	entries, err := fs.Glob(migrationsFS, "migrations/*.sql")
	if err != nil {
		return fmt.Errorf("store: 读取迁移清单: %w", err)
	}
	sort.Strings(entries) // 文件名 0001_init.sql 顺序编号,字典序即应用序

	applied := map[int64]bool{}
	var accountExists int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='schema_migrations'`).Scan(&accountExists); err != nil {
		return fmt.Errorf("store: 探测迁移账表: %w", err)
	}
	if accountExists > 0 {
		rows, err := db.Query(`SELECT version FROM schema_migrations`)
		if err != nil {
			return fmt.Errorf("store: 读迁移账: %w", err)
		}
		for rows.Next() {
			var v int64
			if err := rows.Scan(&v); err != nil {
				_ = rows.Close()
				return fmt.Errorf("store: 扫迁移账: %w", err)
			}
			applied[v] = true
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("store: 遍历迁移账: %w", err)
		}
	}

	for _, name := range entries {
		version, err := versionOf(name)
		if err != nil {
			return err
		}
		if applied[version] {
			continue
		}
		body, err := fs.ReadFile(migrationsFS, name)
		if err != nil {
			return fmt.Errorf("store: 读迁移 %s: %w", name, err)
		}
		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("store: 开迁移事务 %s: %w", name, err)
		}
		if _, err := tx.Exec(string(body)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("store: 应用迁移 %s: %w", name, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
			version, time.Now().UTC().Format(time.RFC3339)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("store: 记迁移账 %s: %w", name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("store: 提交迁移 %s: %w", name, err)
		}
	}
	return nil
}

// versionOf 从文件名取版本号(0001_init.sql → 1);命名不合法即拒绝启动。
func versionOf(name string) (int64, error) {
	base := strings.TrimPrefix(filepath.Base(name), "migrations/")
	digits := base
	if i := strings.IndexByte(base, '_'); i >= 0 {
		digits = base[:i]
	}
	if digits == "" || digits == base && !strings.HasSuffix(base, ".sql") {
		return 0, fmt.Errorf("store: 迁移文件名不合法: %s", name)
	}
	var v int64
	for _, c := range digits {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("store: 迁移版本号非数字: %s", name)
		}
		v = v*10 + int64(c-'0')
	}
	if v <= 0 {
		return 0, fmt.Errorf("store: 迁移版本号须为正: %s", name)
	}
	return v, nil
}

// ---- repo ----

// APIKey api_keys 行的读侧形态(鉴权消费)。TenantStatus 来自 JOIN tenants,
// 供鉴权层一并判定租户禁用(管控面冻结租户必须对数据面生效)。
type APIKey struct {
	ID              string
	TenantID        string
	Name            string
	KeyHash         string
	Prefix          string
	Status          string
	RPMLimit        int64
	TPMLimit        int64
	DailyQuotaMicro int64
	Currency        string
	TenantStatus    string
}

// ErrNotFound 查无此行(哨兵,调用方 errors.Is 判定)。
var ErrNotFound = errors.New("store: 未找到")

// TenantIDByName 按租户名取 id(签发与管理命令解析 --tenant 用)。
func TenantIDByName(db *sql.DB, name string) (string, error) {
	var id string
	err := db.QueryRow(`SELECT id FROM tenants WHERE name = ?`, name).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("store: 查租户: %w", err)
	}
	return id, nil
}

// GetAPIKeyByHash 按 sha256 hex 查 key;吊销行也返回,启用与否由调用方按 Status/TenantStatus 判定。
func GetAPIKeyByHash(db *sql.DB, hash string) (*APIKey, error) {
	row := db.QueryRow(`SELECT k.id, k.tenant_id, k.name, k.key_hash, k.prefix, k.status,
		k.rpm_limit, k.tpm_limit, k.daily_quota_micro, k.currency, IFNULL(t.status,'')
		FROM api_keys k LEFT JOIN tenants t ON t.id = k.tenant_id
		WHERE k.key_hash = ?`, hash)
	var k APIKey
	err := row.Scan(&k.ID, &k.TenantID, &k.Name, &k.KeyHash, &k.Prefix, &k.Status,
		&k.RPMLimit, &k.TPMLimit, &k.DailyQuotaMicro, &k.Currency, &k.TenantStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: 查 api_key: %w", err)
	}
	return &k, nil
}

// UsageRow usage_logs 单行(计量落库消费;字段与 DDL 一一对应)。
type UsageRow struct {
	ID                  string // UUIDv7 主键,重复投递由 INSERT OR IGNORE 兜底幂等
	RequestID           string
	TenantID            string
	APIKeyID            string
	Provider            string
	ModelAlias          string
	UpstreamModel       string
	Stream              bool
	Status              string // ok | error | client_cancel
	ErrorClass          string // 可空
	PromptTokens        int64
	CompletionTokens    int64
	ReasoningTokens     int64
	CachedTokens        int64
	Estimated           bool
	CostMicro           int64
	Currency            string
	LatencyFirstTokenMs *int64 // 可空(未过首 token 的失败请求)
	LatencyTotalMs      int64
	CreatedAt           time.Time // UTC 序列化为 RFC3339
}

// InsertUsageBatch 单事务批量落库;重复主键忽略(背压重投/幂等兜底,metering 契约)。
func InsertUsageBatch(db *sql.DB, rows []UsageRow) error {
	if len(rows) == 0 {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("store: 开计量事务: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // Commit 后为 no-op
	stmt, err := tx.Prepare(`INSERT OR IGNORE INTO usage_logs (
		id, request_id, tenant_id, api_key_id, provider, model_alias, upstream_model,
		stream, status, error_class,
		prompt_tokens, completion_tokens, reasoning_tokens, cached_tokens,
		estimated, cost_micro, currency, latency_first_token_ms, latency_total_ms, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return fmt.Errorf("store: 预编译计量插入: %w", err)
	}
	defer func() { _ = stmt.Close() }()
	for _, r := range rows {
		stream := int64(0)
		if r.Stream {
			stream = 1
		}
		estimated := int64(0)
		if r.Estimated {
			estimated = 1
		}
		var firstToken any
		if r.LatencyFirstTokenMs != nil {
			firstToken = *r.LatencyFirstTokenMs
		}
		if _, err := stmt.Exec(r.ID, r.RequestID, r.TenantID, r.APIKeyID, r.Provider,
			r.ModelAlias, r.UpstreamModel, stream, r.Status, nullIfEmpty(r.ErrorClass),
			r.PromptTokens, r.CompletionTokens, r.ReasoningTokens, r.CachedTokens,
			estimated, r.CostMicro, r.Currency, firstToken, r.LatencyTotalMs,
			r.CreatedAt.UTC().Format(time.RFC3339)); err != nil {
			return fmt.Errorf("store: 写计量行 %s: %w", r.ID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: 提交计量事务: %w", err)
	}
	return nil
}

// SumCostToday 当日已耗成本(μ)聚合,配额判定读侧:
// 仅计 estimated=0 行(metering 对账口径);dayStartUTC 为当日零点(RFC3339 字符串序比较成立)。
// tenantID/apiKeyID 传空串则该维度不过滤(Phase 3 租户聚合复用)。
func SumCostToday(db *sql.DB, tenantID, apiKeyID, dayStartUTC string) (int64, error) {
	q := `SELECT COALESCE(SUM(cost_micro),0) FROM usage_logs
		WHERE estimated = 0 AND created_at >= ?`
	args := []any{dayStartUTC}
	if tenantID != "" {
		q += ` AND tenant_id = ?`
		args = append(args, tenantID)
	}
	if apiKeyID != "" {
		q += ` AND api_key_id = ?`
		args = append(args, apiKeyID)
	}
	var total int64
	if err := db.QueryRow(q, args...).Scan(&total); err != nil {
		return 0, fmt.Errorf("store: 日成本聚合: %w", err)
	}
	return total, nil
}

// nullIfEmpty 空串转 NULL(error_class 等可空 TEXT 列)。
func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
