// 日配额闸(traffic-governance「限流」):key 级与租户聚合级两道闸,
// 已耗成本 = SUM(cost_micro WHERE estimated=0 AND created_at ≥ 当日)(metering 对账口径),
// 进程内缓存 30s——一致性声明:事后记账模型,存在「同刻并发小幅超限」窗口(README FAQ)。
// 超限 → 402 quota_exceeded。
package ratelimit

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/solvela/muniu/internal/auth"
	"github.com/solvela/muniu/internal/logging"
	"github.com/solvela/muniu/internal/protocol"
	"github.com/solvela/muniu/internal/store"
)

// KeyInfoLite 配额判定所需的最小 key/租户信息(中间件从 auth.KeyInfo 转换)。
type KeyInfoLite struct {
	ID              string
	TenantID        string
	DailyQuotaMicro int64 // 0=不限
}

// Options 配额闸参数;零值取默认(TTL 30s)。
type Options struct {
	CacheTTL time.Duration
}

// Quota 日配额判定器(进程内单例)。
type Quota struct {
	db       *sql.DB
	ttl      time.Duration
	now      func() time.Time
	mu       sync.Mutex
	cache    map[string]quotaEntry // scope("k:<id>"/"t:<id>") → 聚合缓存
	dbErrLog *slog.Logger
}

// quotaEntry 缓存条目:任意字段访问须持 q.mu。
type quotaEntry struct {
	sum       int64
	expiresAt time.Time
}

// NewQuota 构造配额闸。logger 供「聚合失败放行」告警(该事件不可观测会让超支无从排查)。
func NewQuota(db *sql.DB, opts Options, logger *slog.Logger) *Quota {
	ttl := opts.CacheTTL
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Quota{db: db, ttl: ttl, now: time.Now, cache: map[string]quotaEntry{}, dbErrLog: logger}
}

// exceeded 判定:先 key 级再租户聚合级,任一超限即拒。额度 0=不限。
func (q *Quota) exceeded(info *KeyInfoLite, now time.Time) bool {
	dayStart := now.UTC().Format("2006-01-02T00:00:00Z")
	if info.DailyQuotaMicro > 0 {
		if q.cachedSum("k:"+info.ID, info.ID, "", dayStart, now) >= info.DailyQuotaMicro {
			return true
		}
	}
	// 租户聚合第二道闸(额度存 tenants.daily_quota_micro,迁移 0002)
	if tq := q.tenantQuota(info.TenantID); tq > 0 {
		if q.cachedSum("t:"+info.TenantID, "", info.TenantID, dayStart, now) >= tq {
			return true
		}
	}
	return false
}

// cachedSum 带 30s TTL 的聚合缓存;调用点须注意:同一请求两道闸各自缓存,口径一致。
func (q *Quota) cachedSum(scope, keyID, tenantID, dayStart string, now time.Time) int64 {
	q.mu.Lock()
	if e, ok := q.cache[scope]; ok && now.Before(e.expiresAt) {
		q.mu.Unlock()
		return e.sum
	}
	q.mu.Unlock()

	sum, err := store.SumCostToday(q.db, tenantID, keyID, dayStart)
	if err != nil {
		// 聚合失败放行并记日志(可用性优先;该失败仅影响配额精度,不影响计量正确性)
		if q.dbErrLog != nil {
			q.dbErrLog.Warn("日配额聚合失败,本次放行", "error_detail", err.Error())
		}
		return 0
	}
	q.mu.Lock()
	q.cache[scope] = quotaEntry{sum: sum, expiresAt: now.Add(q.ttl)}
	q.mu.Unlock()
	return sum
}

// tenantQuota 租户聚合额度(tenants.daily_quota_micro,迁移 0002);查不到按 0(不限)。
func (q *Quota) tenantQuota(tenantID string) int64 {
	var quota int64
	err := q.db.QueryRow(`SELECT daily_quota_micro FROM tenants WHERE id = ?`, tenantID).Scan(&quota)
	if err != nil {
		return 0
	}
	return quota
}

// Middleware 日配额中间件(链序:auth → quota → ratelimit);无鉴权信息的链路直接放行。
func (q *Quota) Middleware(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			info := keyInfoFrom(r.Context())
			if info != nil && q.exceeded(info, time.Now()) {
				logger.Warn("日配额超限", "request_id", logging.RequestIDFrom(r.Context()), "key_id", info.ID)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(protocol.StatusForType(protocol.TypeQuotaExceeded))
				_ = json.NewEncoder(w).Encode(protocol.NewErrorEnvelope(
					protocol.TypeQuotaExceeded, "quota_exceeded", "当日配额已用尽", logging.RequestIDFrom(r.Context())))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// keyInfoFrom ctx 取鉴权信息并裁剪为配额视角(字段显式拷贝,两型字段集不同不可直接转换)。
func keyInfoFrom(ctx context.Context) *KeyInfoLite {
	if info := auth.KeyFrom(ctx); info != nil {
		return &KeyInfoLite{
			ID:              info.ID,
			TenantID:        info.TenantID,
			DailyQuotaMicro: info.DailyQuotaMicro,
		}
	}
	return nil
}
