// 限流和配额中间件遵循 auth → quota → ratelimit → handler 顺序。
package ratelimit

import (
	"encoding/json"
	"math"
	"net/http"
	"strconv"

	"github.com/solvela/muniu/internal/auth"
	"github.com/solvela/muniu/internal/logging"
	"github.com/solvela/muniu/internal/protocol"
)

// Middleware RPM 限流中间件，按 ctx 中的 apikey 限额判定。
// 无鉴权信息的请求（内部探活/无鉴权测试）直接放行——限流维度是 per apikey。
func (l *Limiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		info := auth.KeyFrom(r.Context())
		if info != nil {
			ok, retry := l.Allow(info.ID, info.RPMLimit)
			if !ok {
				secs := int64(math.Ceil(retry.Seconds()))
				if secs < 1 {
					secs = 1
				}
				w.Header().Set("Retry-After", strconv.FormatInt(secs, 10))
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(protocol.StatusForType(protocol.TypeRateLimited))
				_ = json.NewEncoder(w).Encode(protocol.NewErrorEnvelope(
					protocol.TypeRateLimited, "rate_limited", "请求过于频繁,请稍后重试",
					logging.RequestIDFrom(r.Context())))
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// QuotaPlaceholder 保留日配额检查在中间件链中的位置；当前始终放行。
func QuotaPlaceholder(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
	})
}
