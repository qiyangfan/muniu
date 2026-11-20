package admin

import (
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/solvela/muniu/internal/auth"
	"github.com/solvela/muniu/internal/logging"
	"github.com/solvela/muniu/internal/protocol"
)

// adminCookieTTL 与本地账密登录同口径;过期后回到登录页,联邦轨不自动续期
// (续期即隐式重认证,超出 RP 会话语义)。
const adminCookieTTL = 12 * time.Hour

// setAdminCookie 统一会话 cookie 形态:HttpOnly + SameSite=Lax,Secure 随 TLS 动态。
func setAdminCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{Name: adminCookie, Value: token, Path: "/", //nolint:gosec // Secure 随 TLS 动态
		HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteLaxMode, MaxAge: int(adminCookieTTL.Seconds())})
}

// clearAdminCookie 注销与失败退出共用:置空并即刻过期。
func clearAdminCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: adminCookie, Value: "", Path: "/", //nolint:gosec // 同上
		HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteLaxMode, MaxAge: -1})
}

// adminFederationInfo 控制台登录页据此决定是否展示「锦官登录」入口。
func adminFederationInfo(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]bool{"enabled": d.Federated != nil})
	}
}

// adminFederatedStart 浏览器整页跳转入口:发现文档不可达时明确失败,本地/令牌轨不受影响。
func adminFederatedStart(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.Federated == nil {
			writeAdminError(w, r, 404, protocol.TypeNotFound, "federation_disabled", "锦官登录未配置")
			return
		}
		u, err := d.Federated.Begin(r.Context())
		if err != nil {
			d.Logger.Warn("锦官登录发起失败", "request_id", logging.RequestIDFrom(r.Context()), "error_detail", err.Error())
			writeAdminError(w, r, 502, protocol.TypeUpstreamError, "federation_unavailable",
				"锦官登录暂不可用,请使用本地账号或管理令牌")
			return
		}
		http.Redirect(w, r, u, http.StatusFound)
	}
}

// adminFederatedCallback 授权码回调:state 单次凭据,换码验签后签发本地会话。
// 失败一律回控制台登录页并带固定文案,不向浏览器暴露验签/端点细节。
func adminFederatedCallback(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.Federated == nil || d.AdminAuth == nil {
			redirectConsole(w, r, "锦官登录未配置")
			return
		}
		q := r.URL.Query()
		if q.Get("error") != "" {
			redirectConsole(w, r, "锦官登录未完成,请重试")
			return
		}
		if q.Get("code") == "" || q.Get("state") == "" {
			redirectConsole(w, r, "锦官登录回调缺少参数,请重试")
			return
		}
		identity, err := d.Federated.Complete(r.Context(), q.Get("state"), q.Get("code"))
		if err != nil {
			d.Logger.Warn("锦官登录回调失败", "request_id", logging.RequestIDFrom(r.Context()), "error_detail", err.Error())
			redirectConsole(w, r, "锦官登录校验失败,请使用本地账号或管理令牌")
			return
		}
		token, _, err := d.AdminAuth.LoginFederated(r.Context(), identity.Subject, identity.Name)
		if err != nil {
			if errors.Is(err, auth.ErrAdminCredentials) {
				redirectConsole(w, r, "该锦官账号无权登录木牛控制台")
				return
			}
			d.Logger.Error("联邦会话签发失败", "request_id", logging.RequestIDFrom(r.Context()), "error_detail", err.Error())
			writeAdminError(w, r, 500, protocol.TypeInternal, "internal", "内部错误")
			return
		}
		setAdminCookie(w, r, token)
		redirectConsole(w, r, "")
	}
}

// redirectConsole 回控制台;msg 非空时以查询参数携带(登录页展示后自行清除)。
func redirectConsole(w http.ResponseWriter, r *http.Request, msg string) {
	target := "/console/"
	if msg != "" {
		target += "?oidc_error=" + url.QueryEscape(msg)
	}
	http.Redirect(w, r, target, http.StatusFound)
}
