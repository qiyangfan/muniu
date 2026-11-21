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

// Actor 身份沿 request context 传播；共享令牌保留具名但不冒认个人归因。
type Actor struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// ActorFrom 返回当前运维身份，数据面 API key 不进入本上下文。
func ActorFrom(ctx context.Context) Actor { a, _ := ctx.Value(actorKey{}).(Actor); return a }

// exemptPaths 豁免管理面鉴权的请求：OIDC 联邦入口与 OAuth 回调（浏览器整页跳转不带管理凭据）。
// 回调类豁免以 state（不可预测、单次、TTL 内）作持有者凭据，重放/伪造在各自 Flow 被拒。
func exemptPaths(r *http.Request) bool {
	if r.Method == http.MethodPost && r.URL.Path == "/admin/v1/auth/logout/sso" {
		return true // 登出只清自身会话;会话已过期时也须能清 cookie 并跳转
	}
	if r.Method != http.MethodGet {
		return false
	}
	switch r.URL.Path {
	case "/admin/v1/oauth/callback", // 订阅账号授权流回调
		"/admin/v1/auth/federation", // 登录页探测联邦入口是否可用
		"/admin/v1/auth/oidc/start", // 虎符联邦发起(整页跳转)
		"/admin/v1/auth/oidc/callback":
		return true
	}
	return false
}

// resolveActor 认证管理面身份：虎符联邦会话 cookie、共享令牌；失败已写出响应时 ok=false。
// 本地账密轨已删除,会话轨只有虎符一条来源,名字即 id_token 展示名。
func resolveActor(d Deps, w http.ResponseWriter, r *http.Request) (Actor, bool) {
	actor := Actor{}
	if cookie, err := r.Cookie(adminCookie); err == nil && d.AdminAuth != nil {
		p, err := d.AdminAuth.Session(r.Context(), cookie.Value)
		if err == nil {
			actor = Actor{Kind: "federated", Name: p.Username}
		} else if !errors.Is(err, auth.ErrAdminCredentials) {
			respondErr(d, w, r, err, "auth_failed")
			return actor, false
		}
	}
	if actor.Kind == "" && d.Token != "" &&
		secureEqual(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), d.Token) {
		actor = Actor{Kind: "token", Name: "management-token"}
	}
	if actor.Kind == "" {
		d.Logger.Warn("管理面鉴权失败", "request_id", logging.RequestIDFrom(r.Context()))
		writeAdminError(w, r, 401, protocol.TypeAuthFailed, "auth_failed", "管理 token 无效")
		return actor, false
	}
	return actor, true
}

func authOperators(d Deps, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actor := Actor{}
		if !exemptPaths(r) {
			var ok bool
			if actor, ok = resolveActor(d, w, r); !ok {
				return
			}
		} else if r.Method == http.MethodPost && r.URL.Path == "/admin/v1/auth/logout/sso" {
			// 统一登出必须能清掉失效会话;有效会话仍归因到主体,供审计使用。
			actor = optionalSessionActor(d, r)
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), actorKey{}, actor)))
	})
}

// optionalSessionActor 只读解析会话,失败不写响应;供免鉴权但需要审计归因的路径使用。
func optionalSessionActor(d Deps, r *http.Request) Actor {
	if d.AdminAuth == nil {
		return Actor{}
	}
	c, err := r.Cookie(adminCookie)
	if err != nil {
		return Actor{}
	}
	p, err := d.AdminAuth.Session(r.Context(), c.Value)
	if err != nil {
		return Actor{}
	}
	return Actor{Kind: "federated", Name: p.Username}
}

func adminLogout(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(adminCookie); err == nil && d.AdminAuth != nil {
			if err := d.AdminAuth.Logout(r.Context(), c.Value); err != nil {
				respondErr(d, w, r, err, "auth_failed")
				return
			}
		}
		clearAdminCookie(w, d.SecureCookies)
		writeJSON(w, 200, map[string]bool{"ok": true})
	}
}

func adminWhoami(_ Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, ActorFrom(r.Context())) }
}

func adminUsersList(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, err := store.ListAdminPrincipals(r.Context(), d.DB)
		if err != nil {
			respondErr(d, w, r, err, "bad_request")
			return
		}
		writeJSON(w, 200, map[string]any{"admins": p})
	}
}

// adminUsersPatch 按 sub 启停主体;主体只能由虎符首登产生(不再提供本地创建/改密)。
func adminUsersPatch(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Disabled *bool `json:"disabled"`
		}
		if err := decode(r, &in); err != nil || in.Disabled == nil {
			writeAdminError(w, r, 400, protocol.TypeBadRequest, "bad_request", "须提供 disabled")
			return
		}
		err := d.AdminAuth.Patch(r.Context(), r.PathValue("subject"), *in.Disabled)
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
		items, next, err := store.ListAdminAudit(r.Context(), d.DB, r.URL.Query().Get("cursor"), limit)
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

// auditAllowedKeys 允许原样进审计摘要的字段（小写比对）；其余一律记为已省略。
var auditAllowedKeys = map[string]bool{
	"username": true, "name": true, "alias": true, "tenant": true,
	"disabled": true, "enabled": true, "status": true, "currency": true,
	"default": true, "input": true, "output": true, "cached_input": true,
	"timezone": true, "rpm_limit": true, "tpm_limit": true,
	"daily_quota_credits": true, "max_request_cost_credits": true, "expires_at": true,
}

// auditSummaryValue 单字段的审计摘要值：敏感键脱敏，白名单标量原样，其余省略。
func auditSummaryValue(key string, v any) any {
	lower := strings.ToLower(key)
	if strings.Contains(lower, "password") || strings.Contains(lower, "token") || strings.Contains(lower, "secret") {
		return "<redacted>"
	}
	if !auditAllowedKeys[lower] {
		return "<omitted>"
	}
	switch v.(type) {
	case string, json.Number, bool, nil:
		return v
	default:
		return "<omitted>"
	}
}

// decodeAuditBody 严格解码请求体为字段表，多余值或非法 JSON 返回 false。
func decodeAuditBody(body []byte) (map[string]any, bool) {
	var fields map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&fields); err != nil {
		return nil, false
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, false
	}
	return fields, true
}

func auditSummary(body []byte) (string, string) {
	fields, ok := decodeAuditBody(body)
	if !ok {
		return "{}", ""
	}
	username := ""
	for k, v := range fields {
		if strings.EqualFold(k, "username") {
			username, _ = v.(string)
		}
	}
	summary := map[string]any{}
	for k, v := range fields {
		summary[k] = auditSummaryValue(k, v)
	}
	b, err := json.Marshal(summary)
	if err != nil {
		return "{}", username
	}
	return string(b), username
}

// fillPathValues 按匹配到的路由模式回填路径参数，使拒绝出口也能取到 id/prefix 等目标值。
func fillPathValues(r *http.Request, pattern string) {
	parts := strings.Fields(pattern)
	if len(parts) != 2 {
		return
	}
	expected := strings.Split(parts[1], "/")
	actual := strings.Split(r.URL.EscapedPath(), "/")
	if len(expected) != len(actual) {
		return
	}
	for i, segment := range expected {
		if strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "}") {
			value, err := url.PathUnescape(actual[i])
			if err == nil {
				r.SetPathValue(strings.TrimSuffix(strings.TrimPrefix(segment, "{"), "}"), value)
			}
		}
	}
}

// auditAction 由路由模式与请求方法推导动作名，如 keys.patch、auth.logout。
func auditAction(pattern, method string) string {
	parts := strings.Fields(pattern)
	action := strings.ToLower(method)
	if len(parts) != 2 {
		return action
	}
	path := strings.Split(strings.TrimPrefix(parts[1], "/admin/v1/"), "/")
	action = path[0] + "." + strings.ToLower(parts[0])
	if path[0] == "auth" && len(path) > 1 {
		// 保留完整子路径,避免 logout 与 logout/sso、oidc/start 与 oidc/callback 互相覆盖。
		action = "auth." + strings.Join(path[1:], ".")
	}
	return action
}

// auditTarget 审计目标：优先 id，其次 subject/prefix/alias/name 路径参数。
func auditTarget(r *http.Request) string {
	if target := r.PathValue("id"); target != "" {
		return target
	}
	for _, name := range []string{"subject", "prefix", "alias", "name"} {
		if v := r.PathValue(name); v != "" {
			return v
		}
	}
	return ""
}

// auditOutcome 按响应状态定结论；拒绝(401/403)单独记为 denied。
func auditOutcome(status int) string {
	if status < 400 {
		return "ok"
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return "denied"
	}
	return fmt.Sprintf("error:%d", status)
}

// runAudited 执行可 panic 的变更请求：panic 原样上抛，但先把响应状态记为 500 供审计。
func runAudited(aw *auditWriter, r *http.Request, next http.Handler) {
	defer func() {
		if panicValue := recover(); panicValue != nil {
			aw.status = http.StatusInternalServerError
			panic(panicValue)
		}
	}()
	next.ServeHTTP(aw, r)
}

// auditOperators 非 GET 变更请求的审计中间件：读体会留下安全摘要，收尾按实际路由与状态落审计行。
func auditOperators(d Deps, next *http.ServeMux) http.Handler {
	csrf := http.NewCrossOriginProtection()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			next.ServeHTTP(w, r)
			return
		}
		_, pattern := next.Handler(r)
		r.Pattern = pattern
		fillPathValues(r, pattern)
		actor := ActorFrom(r.Context())
		detail := "{}"
		aw := &auditWriter{ResponseWriter: w}
		// 已认证变更与鉴权拒绝共用收尾，读取失败也留下安全摘要。
		defer func() {
			aw.finishAudit(d, r, actor, detail)
		}()
		body, err := io.ReadAll(http.MaxBytesReader(aw, r.Body, 1<<20))
		if err != nil {
			writeAdminError(aw, r, 400, protocol.TypeBadRequest, "bad_request", "管理请求体过大或不可读")
			return
		}
		_ = r.Body.Close()
		r.Body = io.NopCloser(bytes.NewReader(body))
		detail, _ = auditSummary(body)
		// cookie 承载的虎符会话一律过 CSRF;令牌轨不走浏览器凭证
		if actor.Kind == "federated" {
			if err := csrf.Check(r); err != nil {
				writeAdminError(aw, r, 403, protocol.TypeAuthFailed, "cross_origin_denied", "不允许跨站管理操作")
				return
			}
		}
		runAudited(aw, r, next)
	})
}

// finishAudit 收尾落审计行；panic 场景由调用方置 500 由本函数如实记录。
func (aw *auditWriter) finishAudit(d Deps, r *http.Request, actor Actor, detail string) {
	panicValue := recover()
	if panicValue != nil {
		defer func() { panic(panicValue) }()
	}
	// 免鉴权 POST(当前仅统一登出)没有 actor:会话可能已过期,仍须留下动作行。
	if actor.Kind == "" {
		actor = Actor{Kind: "login", Name: "anonymous"}
	}
	status := aw.status
	if panicValue != nil {
		status = http.StatusInternalServerError
	}
	if status == 0 {
		status = 200
	}
	action := auditAction(r.Pattern, r.Method)
	a := store.AdminAudit{
		ID:        uuid.NewV7().String(),
		RequestID: logging.RequestIDFrom(r.Context()),
		ActorKind: actor.Kind,
		ActorName: actor.Name,
		Action:    action,
		TargetID:  auditTarget(r),
		Outcome:   auditOutcome(status),
		Detail:    detail,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if err := store.AppendAdminAudit(r.Context(), d.DB, a); err != nil {
		d.Logger.Error("管理动作审计写入失败", "request_id", a.RequestID, "action", action, "error_detail", err.Error())
	}
}
