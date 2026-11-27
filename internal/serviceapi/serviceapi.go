// 服务只读 API：供锦官只读投影展示服务域配置与运行时摘要。
// 独立服务身份令牌、只读、不返回凭据；无写入口。
package serviceapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/solvela/muniu/internal/protocol"
)

// ServiceID 本服务在只读投影中的稳定标识。
const ServiceID = "muniu"

// TokenVerifier Keycloak 服务令牌校验器；实现见 internal/oidc.ServiceVerifier。
type TokenVerifier interface {
	Verify(ctx context.Context, raw string) (string, error)
}

// Deps 只读摘要来源与身份校验。
type Deps struct {
	Token    string        // 本地 pre-shared，仅接受回环来源
	Verifier TokenVerifier // Keycloak 服务 token 校验；nil 即未启用
	Summary  func(ctx context.Context) (map[string]any, error)
	Logger   *slog.Logger
}

var errNotConfigured = errors.New("serviceapi: summary 未配置")

// New 构造只读 API handler；token 与 Verifier 均空时调用方不应挂载。
func New(d Deps) http.Handler {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /service/v1/summary", d.summary)
	mux.HandleFunc("GET /service/v1/read-projection", d.projection)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ok, reason := d.authorize(r); !ok {
			d.Logger.Debug("服务只读 API 认证失败", "reason", reason)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(protocol.NewErrorEnvelope(
				protocol.TypeAuthFailed, "auth_failed", "服务身份无效", ""))
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// authorize 先试本地令牌(仅回环)，再走 Keycloak 服务令牌。
func (d Deps) authorize(r *http.Request) (bool, string) {
	raw := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if raw == "" {
		return false, "缺少服务身份"
	}
	if d.Token != "" && subtle.ConstantTimeCompare([]byte(raw), []byte(d.Token)) == 1 {
		if isLoopbackAddr(r.RemoteAddr) {
			return true, ""
		}
		return false, "本地服务令牌仅限回环来源"
	}
	if d.Verifier == nil {
		return false, "未配置 Keycloak 服务身份校验"
	}
	if _, err := d.Verifier.Verify(r.Context(), raw); err != nil {
		return false, err.Error()
	}
	return true, ""
}

// isLoopbackAddr 判断直连来源是否为回环地址；不采信 X-Forwarded-For。
func isLoopbackAddr(remote string) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// summary 返回脱敏只读摘要。
func (d Deps) summary(w http.ResponseWriter, r *http.Request) {
	out, err := d.summaryPayload(r)
	if err != nil {
		d.writeError(w, err)
		return
	}
	writeJSON(w, out)
}

// projection 返回锦官 X-01 service-read-projection v1 信封。
func (d Deps) projection(w http.ResponseWriter, r *http.Request) {
	sections, err := d.summaryPayload(r)
	if err != nil {
		d.writeError(w, err)
		return
	}
	writeJSON(w, map[string]any{
		"service_id": ServiceID, "version": "1", "etag": "",
		"observed_at": time.Now().UTC(), "sections": sections,
	})
}

// summaryPayload 生成脱敏摘要；保留 observed_at 供 summary 与 projection 共用。
func (d Deps) summaryPayload(r *http.Request) (map[string]any, error) {
	if d.Summary == nil {
		return nil, errNotConfigured
	}
	out, err := d.Summary(r.Context())
	if err != nil {
		d.Logger.Error("服务只读摘要生成失败", "error_detail", err.Error())
		return nil, err
	}
	out["observed_at"] = time.Now().UTC()
	return out, nil
}

// writeError 输出统一内部错误信封。
func (d Deps) writeError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	message := "内部错误"
	if errors.Is(err, errNotConfigured) {
		status = http.StatusNotFound
		message = "投影未配置"
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(protocol.NewErrorEnvelope(
		protocol.TypeInternal, "internal", message, ""))
}

// writeJSON 输出只读 JSON，不缓存。
func writeJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(payload)
}
