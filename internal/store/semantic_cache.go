// semantic_cache 表 repo(T4.5):存储、按指纹精确查、命中计数、过期清理与启动加载。
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// CacheRow semantic_cache 行(读侧形态)。
type CacheRow struct {
	ID          string
	TenantID    string
	Fingerprint string
	ModelAlias  string
	Embedding   []byte
	Response    []byte
	Hits        int64
	CreatedAt   time.Time
	ExpiresAt   time.Time
}

// UpsertCacheRow 写入/覆盖缓存条目(同 tenant+fingerprint 覆盖)。
func UpsertCacheRow(db *sql.DB, tenantID, fingerprint, modelAlias string, embedding, response []byte, ttl time.Duration) error {
	// 本表时间列用 RFC3339Nano:短 TTL 测试与亚秒边界需要亚秒精度
	now := time.Now().UTC()
	_, err := db.Exec(`INSERT INTO semantic_cache
		(id, tenant_id, fingerprint, model_alias, embedding, response, hits, created_at, expires_at)
		VALUES (?,?,?,?,?,?,0,?,?)
		ON CONFLICT(tenant_id, fingerprint) DO UPDATE SET
		  embedding=excluded.embedding, response=excluded.response,
		  hits=0, created_at=excluded.created_at, expires_at=excluded.expires_at`,
		uuid.Must(uuid.NewV7()).String(), tenantID, fingerprint, modelAlias,
		embedding, response, now.Format(time.RFC3339Nano),
		now.Add(ttl).Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("store: 写语义缓存: %w", err)
	}
	return nil
}

// GetCacheByFingerprint 按(租户,指纹)精确查;过期行视为不存在。
func GetCacheByFingerprint(db *sql.DB, tenantID, fingerprint string, now time.Time) (*CacheRow, error) {
	row := db.QueryRow(`SELECT id, tenant_id, fingerprint, model_alias, embedding, response, hits, created_at, expires_at
		FROM semantic_cache
		WHERE tenant_id = ? AND fingerprint = ? AND expires_at > ?`,
		tenantID, fingerprint, now.UTC().Format(time.RFC3339Nano))
	return scanCacheInto(row)
}

// GetCacheByID 按 id 查(向量 TopK 命中回取)。
func GetCacheByID(db *sql.DB, id string, now time.Time) (*CacheRow, error) {
	row := db.QueryRow(`SELECT id, tenant_id, fingerprint, model_alias, embedding, response, hits, created_at, expires_at
		FROM semantic_cache WHERE id = ? AND expires_at > ?`, id, now.UTC().Format(time.RFC3339Nano))
	return scanCacheInto(row)
}

// TouchCacheHits 命中计数 +1。
func TouchCacheHits(db *sql.DB, id string) error {
	_, err := db.Exec(`UPDATE semantic_cache SET hits = hits + 1 WHERE id = ?`, id)
	return err
}

// DeleteCache 删除条目(LRU 淘汰/清理)。
func DeleteCache(db *sql.DB, id string) error {
	_, err := db.Exec(`DELETE FROM semantic_cache WHERE id = ?`, id)
	return err
}

// DeleteExpiredCache 删除全部已过期条目(TTL 只在读取侧过滤,存储侧靠本调用回收;
// 启动期调用一次即可防止表无限膨胀)。返回删除行数。
func DeleteExpiredCache(db *sql.DB, now time.Time) (int64, error) {
	res, err := db.Exec(`DELETE FROM semantic_cache WHERE expires_at <= ?`, now.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, fmt.Errorf("store: 清理过期语义缓存: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// LoadCacheRows 启动加载全部未过期条目(索引内存态重启重建)。
func LoadCacheRows(db *sql.DB) ([]CacheRow, error) {
	rows, err := db.Query(`SELECT id, tenant_id, fingerprint, model_alias, embedding, response, hits, created_at, expires_at
		FROM semantic_cache WHERE expires_at > ? ORDER BY created_at`, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, fmt.Errorf("store: 加载语义缓存: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []CacheRow
	for rows.Next() {
		r, err := scanCacheInto(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// scanCacheInto 扫描一行缓存条目(单行/多行共用;时间列 RFC3339)。
func scanCacheInto(scanner interface{ Scan(...any) error }) (*CacheRow, error) {
	var r CacheRow
	var created, expires string
	err := scanner.Scan(&r.ID, &r.TenantID, &r.Fingerprint, &r.ModelAlias, &r.Embedding, &r.Response, &r.Hits, &created, &expires)
	if err != nil {
		return nil, err
	}
	r.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	r.ExpiresAt, _ = time.Parse(time.RFC3339Nano, expires)
	return &r, nil
}

// CacheStatsRow 统计(管理 API)。
type CacheStatsRow struct {
	Entries int64
	Hits    int64
}

// CacheStatsRowGet 统计:条数与累计命中。
func CacheStatsRowGet(db *sql.DB) (CacheStatsRow, error) {
	var s CacheStatsRow
	err := db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(hits),0) FROM semantic_cache WHERE expires_at > ?`,
		time.Now().UTC().Format(time.RFC3339Nano)).Scan(&s.Entries, &s.Hits)
	if errors.Is(err, sql.ErrNoRows) {
		return s, nil
	}
	return s, err
}
