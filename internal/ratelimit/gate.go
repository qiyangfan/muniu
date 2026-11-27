// Gate 配额 → RPM → TPM 多闸合一中间件，链序 auth → quota → ratelimit → handler。
// 租户级限额来自控制快照（managed）或本地 tenants 表（standalone）；租户与 key
// 双上限任一超限即拒，租户先判避免被 key 上限掩盖。
package ratelimit

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/solvela/muniu/internal/auth"
	"github.com/solvela/muniu/internal/logging"
	"github.com/solvela/muniu/internal/metrics"
	"github.com/solvela/muniu/internal/protocol"
)

// timeNow 时钟源（包级，测试可换）。
var timeNow = time.Now

// QuotaDenials 配额/限流拒绝计数，按 scope(tenant|key) 与原因分类。
var QuotaDenials = metrics.NewCounterVec(
	"muniu_quota_denials_total", "租户/key 配额与限流拒绝次数", "scope", "reason")

// Gate 流量治理闸门，装配层持有，进程内单例。
type Gate struct {
	RPM       *Limiter
	TPM       *TPM
	TenantRPM *Limiter
	TenantTPM *TPM
	Quota     *Quota
	Logger    *slog.Logger
	// Shared 可选共享计数后端；nil 即进程内 inmem 语义。
	Shared SharedBackend
}

// Middleware 多闸串联，任一拒绝即终止（信封已写）；无鉴权信息的请求直接放行。
func (g *Gate) Middleware(next http.Handler) http.Handler {
	if g.Shared != nil {
		return g.sharedMiddleware(next)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if info := auth.KeyFrom(r.Context()); info != nil && g.denied(w, r, info) {
			return
		}
		next.ServeHTTP(w, r)
	})
}

// denied 依次过日配额、租户 RPM、key RPM、租户 TPM、key TPM，任一拒绝即写信封并返回 true。
func (g *Gate) denied(w http.ResponseWriter, r *http.Request, info *auth.KeyInfo) bool {
	limits, ok := g.tenantLimits(w, r, info)
	if !ok {
		return true
	}
	if g.dailyDenied(w, r, info, limits) || g.localRPMDenied(w, r, info, limits) ||
		g.localTPMDenied(w, r, info, limits) {
		return true
	}
	return false
}

// dailyDenied 日配额（key + 租户）判定。
func (g *Gate) dailyDenied(w http.ResponseWriter, r *http.Request, info *auth.KeyInfo, limits TenantQuota) bool {
	if g.Quota == nil || !g.Quota.exceededWithLimits(r.Context(), keyInfoFrom(r.Context()), limits, timeNow()) {
		return false
	}
	g.observe("daily", "quota", r, info)
	g.denyQuota(w, r)
	return true
}

// localRPMDenied 租户先于 key 的 RPM 判定（inmem）。
func (g *Gate) localRPMDenied(w http.ResponseWriter, r *http.Request, info *auth.KeyInfo, limits TenantQuota) bool {
	if limits.RPM > 0 && g.TenantRPM != nil {
		if ok, _ := g.TenantRPM.Allow("t:"+info.TenantID, limits.RPM); !ok {
			g.observe("tenant", "rpm", r, info)
			g.deny429(w, r, time.Second)
			return true
		}
	}
	if g.RPM == nil {
		return false
	}
	if ok, retry := g.RPM.Allow(info.ID, info.RPMLimit); !ok {
		g.observe("key", "rpm", r, info)
		g.deny429(w, r, retry)
		return true
	}
	return false
}

// localTPMDenied 租户先于 key 的 TPM 判定（inmem）。
func (g *Gate) localTPMDenied(w http.ResponseWriter, r *http.Request, info *auth.KeyInfo, limits TenantQuota) bool {
	now := timeNow()
	if limits.TPM > 0 && g.TenantTPM != nil && !g.TenantTPM.Allow("t:"+info.TenantID, limits.TPM, now) {
		g.observe("tenant", "tpm", r, info)
		g.deny429(w, r, time.Second)
		return true
	}
	if g.TPM != nil && !g.TPM.Allow(info.ID, info.TPMLimit, now) {
		g.observe("key", "tpm", r, info)
		g.deny429(w, r, time.Second) // TPM 无 Retry-After 依据，给 1s
		return true
	}
	return false
}

// tenantLimits 取租户限额；快照缺失/租户未知按安全类失败关闭并写信封。
func (g *Gate) tenantLimits(w http.ResponseWriter, r *http.Request, info *auth.KeyInfo) (TenantQuota, bool) {
	if g.Quota == nil {
		return TenantQuota{}, true
	}
	limits, err := g.Quota.limits(r.Context(), info.TenantID)
	if err == nil {
		if limits.Status == "disabled" {
			g.observe("tenant", "disabled", r, info)
			g.writeDeny(w, r, http.StatusForbidden, protocol.TypeForbidden,
				"tenant_disabled", "租户已被控制面禁用")
			return TenantQuota{}, false
		}
		return limits, true
	}
	g.observe("tenant", "unavailable", r, info)
	if errors.Is(err, ErrTenantUnknown) {
		g.writeDeny(w, r, http.StatusForbidden, protocol.TypeForbidden,
			"tenant_unknown", "租户不在控制面租户目录")
		return TenantQuota{}, false
	}
	g.writeDeny(w, r, http.StatusServiceUnavailable, protocol.TypeUnavailable,
		"quota_snapshot_unavailable", "租户配额快照不可用")
	return TenantQuota{}, false
}

// sharedMiddleware 共享后端路径：链序不变（quota → 租户 RPM → RPM → 租户 TPM → TPM）。
func (g *Gate) sharedMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		info := auth.KeyFrom(r.Context())
		if info == nil {
			next.ServeHTTP(w, r)
			return
		}
		limits, ok := g.tenantLimits(w, r, info)
		if !ok {
			return
		}
		if g.dailyDenied(w, r, info, limits) || g.sharedDenied(w, r, info, limits) {
			return
		}
		next.ServeHTTP(w, r)
	})
}

// sharedDenied 共享视图的租户/key RPM 与 TPM 判定。
func (g *Gate) sharedDenied(w http.ResponseWriter, r *http.Request, info *auth.KeyInfo, limits TenantQuota) bool {
	now := timeNow()
	if limits.RPM > 0 {
		if ok, _ := g.Shared.AllowRPM("t:"+info.TenantID, limits.RPM, now); !ok {
			g.observe("tenant", "rpm", r, info)
			g.deny429(w, r, time.Second)
			return true
		}
	}
	if info.RPMLimit > 0 {
		if ok, retry := g.Shared.AllowRPM(info.ID, info.RPMLimit, now); !ok {
			g.observe("key", "rpm", r, info)
			g.deny429(w, r, retry)
			return true
		}
	}
	if limits.TPM > 0 && !g.Shared.AllowTPM("t:"+info.TenantID, limits.TPM, now) {
		g.observe("tenant", "tpm", r, info)
		g.deny429(w, r, time.Second)
		return true
	}
	if info.TPMLimit > 0 && !g.Shared.AllowTPM(info.ID, info.TPMLimit, now) {
		g.observe("key", "tpm", r, info)
		g.deny429(w, r, time.Second)
		return true
	}
	return false
}

// observe 拒绝可追责：指标按 scope/reason 计数，日志带 request_id/租户/key。
func (g *Gate) observe(scope, reason string, r *http.Request, info *auth.KeyInfo) {
	QuotaDenials.Inc(scope, reason)
	if g.Logger != nil {
		g.Logger.Warn("配额或限流拒绝", "request_id", logging.RequestIDFrom(r.Context()),
			"tenant_id", info.TenantID, "key_id", info.ID, "scope", scope, "reason", reason)
	}
}

// denyQuota 402 信封。
func (g *Gate) denyQuota(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(protocol.StatusForType(protocol.TypeQuotaExceeded))
	_ = json.NewEncoder(w).Encode(protocol.NewErrorEnvelope(
		protocol.TypeQuotaExceeded, "quota_exceeded", "当日配额已用尽", logging.RequestIDFrom(r.Context())))
}

// deny429 429 信封 + Retry-After。
func (g *Gate) deny429(w http.ResponseWriter, r *http.Request, retry time.Duration) {
	secs := int64(retry.Seconds())
	if secs < 1 {
		secs = 1
	}
	w.Header().Set("Retry-After", strconv.FormatInt(secs, 10))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(protocol.StatusForType(protocol.TypeRateLimited))
	_ = json.NewEncoder(w).Encode(protocol.NewErrorEnvelope(
		protocol.TypeRateLimited, "rate_limited", "请求过于频繁,请稍后重试", logging.RequestIDFrom(r.Context())))
}

// writeDeny 非 429/402 的拒绝信封（安全类失败关闭）。
func (g *Gate) writeDeny(w http.ResponseWriter, r *http.Request, status int, typ, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(protocol.NewErrorEnvelope(typ, code, msg, logging.RequestIDFrom(r.Context())))
}
