// 服务只读 API：供锦官只读投影展示服务域配置与运行时摘要。
// 独立服务身份令牌、只读、不返回凭据；无写入口。
package serviceapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/solvela/muniu/internal/protocol"
)

// Deps 只读摘要来源。
type Deps struct {
	Token   string
	Summary func(ctx context.Context) (map[string]any, error)
	Logger  *slog.Logger
}

// New 构造只读 API handler；token 为空时调用方不应挂载。
func New(d Deps) http.Handler {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /service/v1/summary", d.summary)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !authorized(d.Token, r) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(protocol.NewErrorEnvelope(
				protocol.TypeAuthFailed, "auth_failed", "服务身份无效", ""))
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// authorized 恒时比较 Bearer 服务令牌。
func authorized(token string, r *http.Request) bool {
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if got == "" || token == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1
}

// summary 返回脱敏只读摘要。
func (d Deps) summary(w http.ResponseWriter, r *http.Request) {
	if d.Summary == nil {
		http.NotFound(w, r)
		return
	}
	out, err := d.Summary(r.Context())
	if err != nil {
		d.Logger.Error("服务只读摘要生成失败", "error_detail", err.Error())
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(protocol.NewErrorEnvelope(
			protocol.TypeInternal, "internal", "内部错误", ""))
		return
	}
	out["observed_at"] = time.Now().UTC()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(out)
}
