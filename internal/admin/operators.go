package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"uuid"

	"github.com/solvela/muniu/internal/auth"
	"github.com/solvela/muniu/internal/logging"
	"github.com/solvela/muniu/internal/protocol"
	"github.com/solvela/muniu/internal/store"
)

const adminCookie = "muniu_admin_session"

type actorKey struct{}
type loginIdentityKey struct{}
type loginIdentity struct{ name string }

// Actor 身份沿 request context 传播；共享令牌保留具名但不冒认个人归因。
type Actor struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// ActorFrom 返回当前运维身份，数据面 API key 不进入本上下文。
func ActorFrom(ctx context.Context) Actor { a, _ := ctx.Value(actorKey{}).(Actor); return a }

func authOperators(d Deps, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		login := r.Method == http.MethodPost && r.URL.Path == "/admin/v1/auth/login"
		// OAuth 回调豁免:浏览器重定向不带管理凭据;state(不可预测、单次、TTL 内)即回调
		// 的持有者凭据,重放/伪造在 Flow.Exchange 被拒——标准 OAuth 回调姿势。
		oauthCallback := r.Method == http.MethodGet && r.URL.Path == "/admin/v1/oauth/callback"
		actor := Actor{}
		if !login && !oauthCallback {
			if cookie, err := r.Cookie(adminCookie); err == nil && d.AdminAuth != nil {
				u, err := d.AdminAuth.Session(cookie.Value)
				if err == nil {
					actor = Actor{Kind: "session", Name: u.Username}
				} else if !errors.Is(err, auth.ErrAdminCredentials) {
					respondErr(d, w, r, err, "auth_failed")
					return
				}
			}
			if actor.Kind == "" && d.Token != "" && secureEqual(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), d.Token) {
				actor = Actor{Kind: "token", Name: "management-token"}
			}
			if actor.Kind == "" {
				d.Logger.Warn("管理面鉴权失败", "request_id", logging.RequestIDFrom(r.Context()))
				writeAdminError(w, r, 401, protocol.TypeAuthFailed, "auth_failed", "管理 token 无效")
				return
			}
		}

		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), actorKey{}, actor)))
	})
}

func adminLogin(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.AdminAuth == nil {
			writeAdminError(w, r, 401, protocol.TypeAuthFailed, "auth_failed", "管理员凭据无效")
			return
		}
		var in struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := decode(r, &in); err != nil || in.Username == "" || len(in.Username) > 128 {
			writeAdminError(w, r, 400, protocol.TypeBadRequest, "bad_request", "用户名与密码必填")
			return
		}
		if identity, ok := r.Context().Value(loginIdentityKey{}).(*loginIdentity); ok {
			identity.name = in.Username
		}
		token, u, err := d.AdminAuth.Login(in.Username, in.Password)
		if errors.Is(err, auth.ErrAdminCredentials) || errors.Is(err, auth.ErrAdminLocked) {
			writeAdminError(w, r, 401, protocol.TypeAuthFailed, "auth_failed", err.Error())
			return
		}
		if err != nil {
			respondErr(d, w, r, err, "auth_failed")
			return
		}
		http.SetCookie(w, &http.Cookie{Name: adminCookie, Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 12 * 60 * 60})
		writeJSON(w, 200, Actor{Kind: "session", Name: u.Username})
	}
}

func adminLogout(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(adminCookie); err == nil && d.AdminAuth != nil {
			if err := d.AdminAuth.Logout(c.Value); err != nil {
				respondErr(d, w, r, err, "auth_failed")
				return
			}
		}
		http.SetCookie(w, &http.Cookie{Name: adminCookie, Value: "", Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: -1})
		writeJSON(w, 200, map[string]bool{"ok": true})
	}
}

func adminWhoami(_ Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, ActorFrom(r.Context())) }
}

func adminUsersList(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, err := store.ListAdminUsers(d.DB)
		if err != nil {
			respondErr(d, w, r, err, "bad_request")
			return
		}
		writeJSON(w, 200, map[string]any{"admins": u})
	}
}

func adminUsersCreate(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := decode(r, &in); err != nil {
			writeAdminError(w, r, 400, protocol.TypeBadRequest, "bad_request", "请求体非法")
			return
		}
		u, err := d.AdminAuth.Create(in.Username, in.Password)
		if err != nil {
			respondErr(d, w, r, err, "bad_request")
			return
		}
		writeJSON(w, 200, u)
	}
}

func adminUsersPatch(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Disabled *bool   `json:"disabled"`
			Password *string `json:"password"`
		}
		if err := decode(r, &in); err != nil || in.Disabled == nil && in.Password == nil {
			writeAdminError(w, r, 400, protocol.TypeBadRequest, "bad_request", "须提供 disabled 或 password")
			return
		}
		password := ""
		if in.Password != nil {
			password = *in.Password
			if password == "" {
				writeAdminError(w, r, 400, protocol.TypeBadRequest, "bad_request", "密码不可为空")
				return
			}
		}
		err := d.AdminAuth.Patch(r.PathValue("id"), in.Disabled, password)
		if errors.Is(err, store.ErrNotFound) {
			writeAdminError(w, r, 404, protocol.TypeNotFound, "admin_not_found", "管理员不存在")
			return
		}
		if err != nil {
			respondErr(d, w, r, err, "bad_request")
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	}
}

func adminAuditList(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		limit := 50
		if v := r.URL.Query().Get("limit"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				writeAdminError(w, r, 400, protocol.TypeBadRequest, "bad_request", "limit 非法")
				return
			}
			limit = n
		}
		items, next, err := store.ListAdminAudit(d.DB, r.URL.Query().Get("cursor"), limit)
		if err != nil {
			respondErr(d, w, r, err, "bad_request")
			return
		}
		writeJSON(w, 200, map[string]any{"items": items, "next_cursor": next})
	}
}

type auditWriter struct {
	http.ResponseWriter
	status int
}

func (w *auditWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
func (w *auditWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	return w.ResponseWriter.Write(b)
}

func auditSummary(body []byte) (string, string) {
	var fields map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&fields); err != nil {
		return "{}", ""
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return "{}", ""
	}
	username := ""
	for k, v := range fields {
		if strings.EqualFold(k, "username") {
			username, _ = v.(string)
		}
	}
	allowed := map[string]bool{"username": true, "name": true, "alias": true, "tenant": true, "disabled": true, "enabled": true, "status": true, "currency": true, "default": true, "input": true, "output": true, "cached_input": true, "timezone": true, "rpm_limit": true, "tpm_limit": true, "daily_quota_credits": true, "max_request_cost_credits": true, "expires_at": true}
	summary := map[string]any{}
	for k, v := range fields {
		lower := strings.ToLower(k)
		switch {
		case strings.Contains(lower, "password") || strings.Contains(lower, "token") || strings.Contains(lower, "secret"):
			summary[k] = "<redacted>"
		case allowed[lower]:
			switch v.(type) {
			case string, json.Number, bool, nil:
				summary[k] = v
			default:
				summary[k] = "<omitted>"
			}
		default:
			summary[k] = "<omitted>"
		}
	}
	b, err := json.Marshal(summary)
	if err != nil {
		return "{}", username
	}
	return string(b), username
}

func auditOperators(d Deps, next *http.ServeMux) http.Handler {
	csrf := http.NewCrossOriginProtection()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			next.ServeHTTP(w, r)
			return
		}
		// 提前匹配路由，拒绝出口也按实际路由归因，而非原始 path 猜动作。
		_, pattern := next.Handler(r)
		r.Pattern = pattern
		parts := strings.Fields(pattern)
		if len(parts) == 2 {
			expected := strings.Split(parts[1], "/")
			actual := strings.Split(r.URL.EscapedPath(), "/")
			if len(expected) == len(actual) {
				for i, segment := range expected {
					if strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "}") {
						value, err := url.PathUnescape(actual[i])
						if err == nil {
							r.SetPathValue(strings.TrimSuffix(strings.TrimPrefix(segment, "{"), "}"), value)
						}
					}
				}
			}
		}
		actor := ActorFrom(r.Context())
		login := r.Method == http.MethodPost && r.URL.Path == "/admin/v1/auth/login"
		identity := &loginIdentity{}
		r = r.WithContext(context.WithValue(r.Context(), loginIdentityKey{}, identity))
		detail := "{}"
		aw := &auditWriter{ResponseWriter: w}
		// 所有已认证变更与登录拒绝共用收尾，读取失败也留下安全摘要。
		defer func() {
			panicValue := recover()
			if panicValue != nil {
				defer func() { panic(panicValue) }()
			}
			if login {
				actor = Actor{Kind: "login", Name: identity.name}
			}
			status := aw.status
			if panicValue != nil {
				status = http.StatusInternalServerError
			}
			if status == 0 {
				status = 200
			}
			outcome := "ok"
			if status >= 400 {
				outcome = fmt.Sprintf("error:%d", status)
				if login {
					outcome = "denied"
				}
			}
			pattern := r.Pattern
			parts := strings.Fields(pattern)
			action := strings.ToLower(r.Method)
			if len(parts) == 2 {
				path := strings.Split(strings.TrimPrefix(parts[1], "/admin/v1/"), "/")
				action = path[0] + "." + strings.ToLower(parts[0])
				if path[0] == "auth" && len(path) > 1 {
					action = "auth." + path[1]
				}
			}
			if login {
				action = "auth.login"
			}
			target := r.PathValue("id")
			if target == "" {
				for _, name := range []string{"prefix", "alias", "name"} {
					if v := r.PathValue(name); v != "" {
						target = v
						break
					}
				}
			}
			a := store.AdminAudit{ID: uuid.NewV7().String(), RequestID: logging.RequestIDFrom(r.Context()), ActorKind: actor.Kind, ActorName: actor.Name, Action: action, TargetID: target, Outcome: outcome, Detail: detail, CreatedAt: time.Now().UTC().Format(time.RFC3339)}
			if err := store.AppendAdminAudit(d.DB, a); err != nil {
				d.Logger.Error("管理动作审计写入失败", "request_id", a.RequestID, "action", action, "error_detail", err.Error())
			}
		}()
		body, err := io.ReadAll(http.MaxBytesReader(aw, r.Body, 1<<20))
		if err != nil {
			writeAdminError(aw, r, 400, protocol.TypeBadRequest, "bad_request", "管理请求体过大或不可读")
			return
		}
		_ = r.Body.Close()
		r.Body = io.NopCloser(bytes.NewReader(body))
		detail, identity.name = auditSummary(body)
		if login || actor.Kind == "session" {
			if err := csrf.Check(r); err != nil {
				writeAdminError(aw, r, 403, protocol.TypeAuthFailed, "cross_origin_denied", "不允许跨站管理操作")
				return
			}
		}
		next.ServeHTTP(aw, r)
	})
}
