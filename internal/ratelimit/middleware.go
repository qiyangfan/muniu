// 限流/配额中间件(architecture 中间件链:auth → quota → ratelimit → handler)。
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

// Middleware RPM 限流中间件:按 ctx 中的 apikey 限额判定。
// 无鉴权信息的链路(内部探活/无鉴权测试)直接放行——限流维度是 per apikey。
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

// QuotaPlaceholder 日配额占位中间件(Phase 3 落地硬闸:已耗成本 ≥ daily_quota_micro → 402)。
// 当前永放行——占位是为了把中间件链顺序先钉死(顺序即契约)。
func QuotaPlaceholder(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
	})
}
