// 日配额闸契约测试(测试先行域):key 级边界恰差 1μ、租户聚合第二道闸、缓存 30s 过期后生效。
// 时间注入,完全确定。
package ratelimit

import (
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/solvela/muniu/internal/store"
)

const quotaTenant = "01900000-0000-7000-8000-000000000001"

// seedQuota 开临时库并插入一把 key(quota=100μ);tenantQuota 为租户聚合额度(0=不限)。
func seedQuota(t *testing.T, tenantQuota int64) *Quota {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "quota.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.Exec(`INSERT INTO api_keys (id, tenant_id, name, key_hash, prefix, status, daily_quota_micro, created_at)
		VALUES (?,?,?,?,?,?,?,?)`,
		uuid.Must(uuid.NewV7()).String(), quotaTenant, "k", "hash-quota", "mk-quota", "active", 100, now); err != nil {
		t.Fatal(err)
	}
	// 租户聚合闸(迁移 0002 加列)
	if _, err := db.Exec(`UPDATE tenants SET daily_quota_micro = ? WHERE id = ?`, tenantQuota, quotaTenant); err != nil {
		t.Fatal(err)
	}
	return NewQuota(db, Options{CacheTTL: 30 * time.Second}, slog.Default())
}

func TestQuota_key级边界恰差1微单位(t *testing.T) {
	q := seedQuota(t, 0) // 租户不限,专注 key 级边界
	// 聚合缓存会掩盖同窗口内的增量,故把 TTL 压到 1ms 验证纯边界语义
	q.ttl = time.Millisecond
	now := time.Now()
	info := &KeyInfoLite{ID: "key-1", TenantID: quotaTenant, DailyQuotaMicro: 100}

	// 已耗 99μ:放行
	insertUsage(t, q, "k-1", quotaTenant, "key-1", 99, now)
	if exceeded := q.exceeded(info, now); exceeded {
		t.Fatal("99/100 应放行")
	}
	// 已耗 100μ:拒绝(恰差 1μ 边界)
	insertUsage(t, q, "k-2", quotaTenant, "key-1", 1, now)
	time.Sleep(2 * time.Millisecond) // 等缓存过期
	if exceeded := q.exceeded(info, now.Add(5*time.Millisecond)); !exceeded {
		t.Fatal("100/100 应拒绝(边界恰差 1μ)")
	}
}

func TestQuota_缓存30秒过期后生效(t *testing.T) {
	q := seedQuota(t, 0)
	now := time.Now()
	info := &KeyInfoLite{ID: "key-1", TenantID: quotaTenant, DailyQuotaMicro: 100}
	insertUsage(t, q, "k-1", quotaTenant, "key-1", 100, now)
	if exceeded := q.exceeded(info, now); !exceeded {
		t.Fatal("100/100 应拒绝")
	}
	// TTL 内(缓存未过期):清空 usage 也不影响判定
	q.clearUsage()
	if exceeded := q.exceeded(info, now.Add(time.Second)); !exceeded {
		t.Fatal("缓存 TTL 内应仍拒绝(一致性窗口语义)")
	}
	// TTL 过期:缓存失效 → 重新聚合 → 放行
	if exceeded := q.exceeded(info, now.Add(31*time.Second)); exceeded {
		t.Fatal("缓存过期后应重新聚合并放行")
	}
}

func TestQuota_租户聚合第二道闸(t *testing.T) {
	q := seedQuota(t, 50)
	now := time.Now()
	// key 自己额度 100 未超,但租户聚合 50 已超 → 拒
	insertUsage(t, q, "k-1", quotaTenant, "key-1", 30, now)
	insertUsage(t, q, "k-2", quotaTenant, "key-2", 25, now)
	info := &KeyInfoLite{ID: "key-1", TenantID: quotaTenant, DailyQuotaMicro: 100}
	if exceeded := q.exceeded(info, now); !exceeded {
		t.Fatal("租户聚合 55/50 超限应拒绝(key 未超)")
	}
}

func TestQuota_quota0不限(t *testing.T) {
	q := seedQuota(t, 0)
	now := time.Now()
	insertUsage(t, q, "k-1", quotaTenant, "key-1", 1_000_000, now)
	info := &KeyInfoLite{ID: "key-1", TenantID: quotaTenant, DailyQuotaMicro: 0}
	if exceeded := q.exceeded(info, now); exceeded {
		t.Fatal("quota=0 视为不限")
	}
}

// ---- 测试辅助 ----

func insertUsage(t *testing.T, q *Quota, reqID, tenantID, keyID string, cost int64, at time.Time) {
	t.Helper()
	row := store.UsageRow{
		ID: uuid.Must(uuid.NewV7()).String(), RequestID: reqID,
		TenantID: tenantID, APIKeyID: keyID,
		Provider: "p", ModelAlias: "m", UpstreamModel: "u",
		Status: "ok", CostMicro: cost, Currency: "CNY",
		LatencyTotalMs: 1, CreatedAt: at.UTC(),
	}
	if err := store.InsertUsageBatch(q.db, []store.UsageRow{row}); err != nil {
		t.Fatal(err)
	}
}

func (q *Quota) clearUsage() {
	_, _ = q.db.Exec(`DELETE FROM usage_logs`)
}
