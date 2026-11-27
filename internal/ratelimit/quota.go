// 日配额与租户限额读取：key 级与租户聚合级两道闸。
// 已耗成本 = SUM(cost_credits WHERE estimated=0 AND created_at ≥ 当日)，与计量侧对账口径一致；
// 进程内缓存 30s。事后记账模型，存在同刻并发小幅超限的一致性窗口。
// managed 下租户限额来自控制快照：快照未就绪或租户缺席一律拒绝（安全类 fail-closed）。
package ratelimit

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/solvela/muniu/internal/auth"
	"github.com/solvela/muniu/internal/store"
)

// ErrQuotaUnavailable 控制面租户配额快照缺失/非法：安全类失败关闭。
var ErrQuotaUnavailable = errors.New("ratelimit: 租户配额快照不可用")

// ErrTenantUnknown 快照已就绪但租户不在目录：按未知租户拒绝，不回落为不限。
var ErrTenantUnknown = errors.New("ratelimit: 租户不在控制面租户目录")

// TenantQuota 租户级限额；0=不限。
type TenantQuota struct {
	RPM               int64
	TPM               int64
	DailyQuotaCredits int64
}

// TenantQuotaSource 控制面租户配额来源；Ready=false 时 Gate 拒绝全部已鉴权请求。
type TenantQuotaSource interface {
	Quota(tenantID string) (TenantQuota, bool)
	Ready() bool
}

// KeyInfoLite 配额判定所需的最小 key/租户信息，由中间件从 auth.KeyInfo 转换。
type KeyInfoLite struct {
	ID                string
	TenantID          string
	DailyQuotaCredits int64 // 0=不限
}

// Options 配额闸参数，零值取默认 TTL 30s；Source 非 nil 即 managed 快照来源。
type Options struct {
	CacheTTL time.Duration
	Source   TenantQuotaSource
}

// Quota 日配额判定器，进程内单例。
type Quota struct {
	db       *sql.DB
	ttl      time.Duration
	now      func() time.Time
	mu       sync.Mutex
	cache    map[string]quotaEntry // scope("k:<id>"/"t:<id>") → 聚合缓存
	dbErrLog *slog.Logger
	source   TenantQuotaSource
}

// quotaEntry 缓存条目，任意字段访问须持 q.mu。
type quotaEntry struct {
	sum       int64
	expiresAt time.Time
}

// NewQuota 构造配额闸。logger 供「聚合失败放行」告警，该事件不可观测会让超支无从排查。
func NewQuota(db *sql.DB, opts Options, logger *slog.Logger) *Quota {
	ttl := opts.CacheTTL
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Quota{db: db, ttl: ttl, now: time.Now, cache: map[string]quotaEntry{},
		dbErrLog: logger, source: opts.Source}
}

// exceeded 判定：先取租户限额，再判 key 日配额与租户日配额，任一超限即拒。
func (q *Quota) exceeded(ctx context.Context, info *KeyInfoLite, now time.Time) (bool, error) {
	_, exceeded, err := q.evaluate(ctx, info, now)
	return exceeded, err
}

// evaluate 返回本次请求适用的租户限额与日配额判定；限额获取失败即失败关闭。
func (q *Quota) evaluate(ctx context.Context, info *KeyInfoLite, now time.Time) (TenantQuota, bool, error) {
	limits, err := q.limits(ctx, info.TenantID)
	if err != nil {
		return TenantQuota{}, false, err
	}
	return limits, q.exceededWithLimits(ctx, info, limits, now), nil
}

// exceededWithLimits 用已取得的租户限额做日配额判定（Gate 与限额查询共用一次读取）。
func (q *Quota) exceededWithLimits(ctx context.Context, info *KeyInfoLite, limits TenantQuota, now time.Time) bool {
	dayStart := now.UTC().Format("2006-01-02T00:00:00Z")
	if info.DailyQuotaCredits > 0 {
		if q.cachedSum(ctx, "k:"+info.ID, info.ID, "", dayStart, now) >= info.DailyQuotaCredits {
			return true
		}
	}
	if limits.DailyQuotaCredits > 0 {
		if q.cachedSum(ctx, "t:"+info.TenantID, "", info.TenantID, dayStart, now) >= limits.DailyQuotaCredits {
			return true
		}
	}
	return false
}

// limits 取租户限额：快照来源要求 Ready 且租户在场；standalone 走本地 tenants 表。
func (q *Quota) limits(ctx context.Context, tenantID string) (TenantQuota, error) {
	if q.source != nil {
		if !q.source.Ready() {
			return TenantQuota{}, ErrQuotaUnavailable
		}
		qt, ok := q.source.Quota(tenantID)
		if !ok {
			return TenantQuota{}, ErrTenantUnknown
		}
		return qt, nil
	}
	return q.dbLimits(ctx, tenantID), nil
}

// dbLimits standalone 租户限额（daily_quota_credits/tpm_limit）；查询失败按既有可用性优先放行。
func (q *Quota) dbLimits(ctx context.Context, tenantID string) TenantQuota {
	var daily, tpm int64
	err := q.db.QueryRowContext(ctx,
		`SELECT daily_quota_credits, tpm_limit FROM tenants WHERE id = ?`, tenantID).Scan(&daily, &tpm)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) && q.dbErrLog != nil {
			q.dbErrLog.Warn("租户限额查询失败,本次按不限处理", "error_detail", err.Error())
		}
		return TenantQuota{}
	}
	return TenantQuota{TPM: tpm, DailyQuotaCredits: daily}
}

// cachedSum 带 30s TTL 的聚合缓存；同一请求两道闸各自缓存，口径一致。
func (q *Quota) cachedSum(ctx context.Context, scope, keyID, tenantID, dayStart string, now time.Time) int64 {
	q.mu.Lock()
	if e, ok := q.cache[scope]; ok && now.Before(e.expiresAt) {
		q.mu.Unlock()
		return e.sum
	}
	q.mu.Unlock()

	sum, err := store.SumCostToday(ctx, q.db, tenantID, keyID, dayStart)
	if err != nil {
		// 聚合失败放行并记日志：可用性优先，该失败仅影响配额精度，不影响计量正确性
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

// keyInfoFrom 从 ctx 取鉴权信息并裁剪为配额视角，字段显式拷贝——两型字段集不同，不可直接转换。
func keyInfoFrom(ctx context.Context) *KeyInfoLite {
	if info := auth.KeyFrom(ctx); info != nil {
		return &KeyInfoLite{
			ID:                info.ID,
			TenantID:          info.TenantID,
			DailyQuotaCredits: info.DailyQuotaCredits,
		}
	}
	return nil
}

// MemoryTenantQuotas 快照投影的进程内租户目录；Replace 原子替换，Ready 独立控制。
type MemoryTenantQuotas struct {
	mu    sync.RWMutex
	m     map[string]TenantQuota
	ready bool
}

// NewMemoryTenantQuotas 构造；ready=false 表示尚无有效快照。
func NewMemoryTenantQuotas() *MemoryTenantQuotas {
	return &MemoryTenantQuotas{m: map[string]TenantQuota{}}
}

// Replace 原子替换目录并置为就绪。
func (s *MemoryTenantQuotas) Replace(quotas map[string]TenantQuota) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m = quotas
	s.ready = true
}

// Quota 取租户限额，未命中返回 ok=false（调用方按未知租户失败关闭）。
func (s *MemoryTenantQuotas) Quota(tenantID string) (TenantQuota, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	qt, ok := s.m[tenantID]
	return qt, ok
}

// Ready 是否已有有效快照。
func (s *MemoryTenantQuotas) Ready() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ready
}
