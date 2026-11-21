package admin

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"time"
	"uuid"

	"github.com/solvela/muniu/internal/auth"
	"github.com/solvela/muniu/internal/logging"
	"github.com/solvela/muniu/internal/protocol"
	"github.com/solvela/muniu/internal/store"
)

// adminCookieTTL 固定会话期;过期后回到登录页,联邦轨不自动续期
// (续期即隐式重认证,超出 RP 会话语义)。
const adminCookieTTL = 12 * time.Hour

// oidcStateCookie 把 state 绑定到发起授权的浏览器:回调 URL 被转交他人也无法完成登录(登录 CSRF)。
const (
	oidcStateCookie = "muniu_oidc_state"
	oidcStateTTL    = 10 * time.Minute
)

// federationLoginRPM 登录发起与回调的每 IP 每分钟上限:匿名入口,须防状态表/出站被刷。
const federationLoginRPM = 30

// federationLoginAllowed 按客户端 IP 限流;不信任 X-Forwarded-For,反代场景由前置层另有约束。
func federationLoginAllowed(d Deps, r *http.Request) bool {
	if d.LoginLimiter == nil {
		return true
	}
	ip := r.RemoteAddr
	if host, _, err := net.SplitHostPort(ip); err == nil {
		ip = host
	}
	ok, _ := d.LoginLimiter.Allow("login:"+ip, federationLoginRPM)
	return ok
}

// setAdminCookie 统一会话 cookie 形态:HttpOnly + SameSite=Lax;Secure 由部署配置决定,
// 反代终止 TLS 时 r.TLS 恒为 nil,不能据此推断。
func setAdminCookie(w http.ResponseWriter, token string, secure bool) {
	http.SetCookie(w, &http.Cookie{Name: adminCookie, Value: token, Path: "/", //nolint:gosec // Secure 由部署配置决定
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode, MaxAge: int(adminCookieTTL.Seconds())})
}

// clearAdminCookie 注销与失败退出共用:置空并即刻过期。
func clearAdminCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{Name: adminCookie, Value: "", Path: "/", //nolint:gosec // 同上
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode, MaxAge: -1})
}

// adminFederationInfo 控制台登录页据此决定是否展示「统一身份登录」入口。
func adminFederationInfo(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]bool{"enabled": d.Federated != nil})
	}
}

// adminFederatedStart 浏览器整页跳转入口:发现文档不可达时明确失败,令牌轨不受影响。
func adminFederatedStart(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.Federated == nil {
			writeAdminError(w, r, 404, protocol.TypeNotFound, "federation_disabled", "统一身份登录未配置")
			return
		}
		if rejectFederationRateLimited(d, w, r) {
			return
		}
		u, err := d.Federated.Begin(r.Context())
		if err != nil {
			d.Logger.Warn("统一身份登录发起失败", "request_id", logging.RequestIDFrom(r.Context()), "error_detail", err.Error())
			writeAdminError(w, r, 502, protocol.TypeUpstreamError, "federation_unavailable",
				"统一身份登录暂不可用,请使用管理令牌")
			return
		}
		target, err := url.Parse(u)
		if err != nil || target.Query().Get("state") == "" {
			d.Logger.Error("统一身份登录授权 URL 缺 state", "request_id", logging.RequestIDFrom(r.Context()))
			writeAdminError(w, r, 500, protocol.TypeInternal, "internal", "内部错误")
			return
		}
		setOIDCStateCookie(w, target.Query().Get("state"), d.SecureCookies)
		http.Redirect(w, r, u, http.StatusFound)
	}
}

// setOIDCStateCookie 绑定发起授权与回调浏览器;Path 收敛到回调前缀,避免随业务请求携带。
func setOIDCStateCookie(w http.ResponseWriter, state string, secure bool) {
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // Secure 由部署配置决定
		Name: oidcStateCookie, Value: state, Path: "/admin/v1/auth/oidc",
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode,
		MaxAge: int(oidcStateTTL.Seconds()),
	})
}

// clearOIDCStateCookie 回调无论成败都即刻失效,失败重试需重新发起授权。
func clearOIDCStateCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // 同上
		Name: oidcStateCookie, Value: "", Path: "/admin/v1/auth/oidc",
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode, MaxAge: -1,
	})
}

// stateCookieMatches 恒定时间比较,阻断回调 URL 被转交他人的登录 CSRF。
func stateCookieMatches(r *http.Request, state string) bool {
	c, err := r.Cookie(oidcStateCookie)
	if err != nil || c.Value == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(c.Value), []byte(state)) == 1
}

// adminFederatedCallback 授权码回调:state 单次凭据,换码验签后按虎符 sub 建取主体并签发本地会话。
// 失败一律回控制台登录页并带固定文案,不向浏览器暴露验签/端点细节。
func adminFederatedCallback(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		subject, name, ok := federatedIdentity(d, w, r)
		if !ok {
			return
		}
		token, _, err := d.AdminAuth.LoginFederated(r.Context(), subject, name)
		if err != nil {
			if errors.Is(err, auth.ErrAdminCredentials) {
				auditOIDCLogin(d, r, subject, "denied", "subject_not_allowed_or_disabled")
				redirectConsole(w, r, "该虎符主体无权登录木牛控制台")
				return
			}
			auditOIDCLogin(d, r, subject, "error", "session_issue_failed")
			d.Logger.Error("联邦会话签发失败", "request_id", logging.RequestIDFrom(r.Context()), "error_detail", err.Error())
			writeAdminError(w, r, 500, protocol.TypeInternal, "internal", "内部错误")
			return
		}
		setAdminCookie(w, token, d.SecureCookies)
		auditOIDCLogin(d, r, subject, "ok", "")
		redirectConsole(w, r, "")
	}
}

// federatedIdentity 完成回调前的全部校验与换码;失败已写出响应并返回 ok=false。
func federatedIdentity(d Deps, w http.ResponseWriter, r *http.Request) (subject, name string, ok bool) {
	if d.Federated == nil || d.AdminAuth == nil {
		redirectConsole(w, r, "统一身份登录未配置")
		return "", "", false
	}
	if rejectFederationRateLimited(d, w, r) {
		return "", "", false
	}
	q := r.URL.Query()
	clearOIDCStateCookie(w, d.SecureCookies)
	if q.Get("error") != "" {
		auditOIDCLogin(d, r, "", "denied", "upstream_error")
		redirectConsole(w, r, "统一身份登录未完成,请重试")
		return "", "", false
	}
	if q.Get("code") == "" || q.Get("state") == "" {
		auditOIDCLogin(d, r, "", "denied", "missing_params")
		redirectConsole(w, r, "统一身份登录回调缺少参数,请重试")
		return "", "", false
	}
	if !stateCookieMatches(r, q.Get("state")) {
		auditOIDCLogin(d, r, "", "denied", "state_mismatch")
		redirectConsole(w, r, "统一身份登录校验失败,请使用管理令牌")
		return "", "", false
	}
	identity, err := d.Federated.Complete(r.Context(), q.Get("state"), q.Get("code"))
	if err != nil {
		auditOIDCLogin(d, r, "", "denied", "state_or_token_invalid")
		d.Logger.Warn("统一身份登录回调失败", "request_id", logging.RequestIDFrom(r.Context()), "error_detail", err.Error())
		redirectConsole(w, r, "统一身份登录校验失败,请使用管理令牌")
		return "", "", false
	}
	return identity.Subject, identity.Name, true
}

// rejectFederationRateLimited 匿名登录入口超限即写 429,调用方直接返回。
func rejectFederationRateLimited(d Deps, w http.ResponseWriter, r *http.Request) bool {
	if federationLoginAllowed(d, r) {
		return false
	}
	writeAdminError(w, r, 429, protocol.TypeRateLimited, "too_many_requests", "请求过于频繁,请稍后再试")
	return true
}

// auditOIDCLogin 记录联邦登录结果:回调是 GET,不经过管理面变更审计,必须在此显式留痕。
func auditOIDCLogin(d Deps, r *http.Request, subject, outcome, reason string) {
	if d.DB == nil {
		return
	}
	name := subject
	if name == "" {
		name = "hufu"
	}
	detail, _ := json.Marshal(map[string]string{"provider": "hufu", "reason": reason})
	a := store.AdminAudit{
		ID:        uuid.NewV7().String(),
		RequestID: logging.RequestIDFrom(r.Context()),
		ActorKind: "login",
		ActorName: name,
		Action:    "auth.oidc.login",
		TargetID:  subject,
		Outcome:   outcome,
		Detail:    string(detail),
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if err := store.AppendAdminAudit(r.Context(), d.DB, a); err != nil {
		d.Logger.Error("联邦登录审计写入失败", "request_id", a.RequestID, "error_detail", err.Error())
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

// adminLogoutSSO 注销本地会话并给出虎符 end_session 地址;权威不可达时退回控制台,不留半注销态。
func adminLogoutSSO(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(adminCookie); err == nil && d.AdminAuth != nil {
			if err := d.AdminAuth.Logout(r.Context(), c.Value); err != nil {
				respondErr(d, w, r, err, "auth_failed")
				return
			}
		}
		clearAdminCookie(w, d.SecureCookies)
		target := "/console/"
		if d.Federated != nil {
			redirect := d.PostLogoutRedirect
			if redirect == "" {
				redirect = defaultPostLogoutRedirect(d)
			}
			if u, err := d.Federated.EndSessionURL(r.Context(), redirect); err != nil {
				d.Logger.Warn("虎符登出地址解析失败,退回本地注销", "request_id", logging.RequestIDFrom(r.Context()),
					"error_detail", err.Error())
			} else {
				target = u
			}
		}
		writeJSON(w, 200, map[string]string{"redirect_url": target})
	}
}

// defaultPostLogoutRedirect 未显式声明时按 client 同源推导控制台地址;仍需在虎符登记后才被接受。
func defaultPostLogoutRedirect(d Deps) string {
	if d.Federated == nil {
		return ""
	}
	base, err := url.Parse(d.Federated.RedirectURL())
	if err != nil || base.Scheme == "" || base.Host == "" {
		return ""
	}
	base.Path, base.RawQuery, base.Fragment = "/console/", "", ""
	return base.String()
}
