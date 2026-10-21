// Gate 配额 → RPM → TPM 三闸合一中间件，链序 auth → quota → ratelimit → handler。
// TPM 前置判定用 key 的 tpm_limit；记账由 proxy 在请求完成时回填 ratelimit.TPM.Record。
package ratelimit

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/solvela/muniu/internal/auth"
	"github.com/solvela/muniu/internal/logging"
	"github.com/solvela/muniu/internal/protocol"
)

// timeNow 时钟源（包级，测试可换）。
var timeNow = time.Now

// Gate 流量治理闸门，装配层持有，进程内单例。
type Gate struct {
	RPM    *Limiter
	TPM    *TPM
	Quota  *Quota
	Logger *slog.Logger
	// Shared 可选共享计数后端；nil 即进程内 inmem 语义。
	Shared SharedBackend
}

// Middleware 三闸串联，任一拒绝即终止（信封已写）；无鉴权信息的请求直接放行。
func (g *Gate) Middleware(next http.Handler) http.Handler {
	if g.Shared != nil {
		return g.sharedMiddleware(next)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		info := auth.KeyFrom(r.Context())
		if info != nil {
			// 1) 日配额（key 级 + 租户聚合）
			if g.Quota != nil && g.Quota.exceeded(keyInfoFrom(r.Context()), timeNow()) {
				g.denyQuota(w, r)
				return
			}
			// 2) RPM 令牌桶
			if g.RPM != nil {
				if ok, retry := g.RPM.Allow(info.ID, info.RPMLimit); !ok {
					g.deny429(w, r, retry)
					return
				}
			}
			// 3) TPM 滑动窗口
			if g.TPM != nil && !g.TPM.Allow(info.ID, info.TPMLimit, timeNow()) {
				g.deny429(w, r, time.Second) // TPM 无 Retry-After 依据，给 1s
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// denyQuota 402 信封。
func (g *Gate) denyQuota(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(protocol.StatusForType(protocol.TypeQuotaExceeded))
	_ = json.NewEncoder(w).Encode(protocol.NewErrorEnvelope(
		protocol.TypeQuotaExceeded, "quota_exceeded", "当日配额已用尽", logging.RequestIDFrom(r.Context())))
}

// denyRPM 429 信封 + Retry-After。
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
