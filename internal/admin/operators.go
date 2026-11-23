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
	"slices"
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

// Actor 身份与范围沿 request context 传播；共享令牌保留具名但不冒认个人归因。
// TenantIDs 是 TENANT 范围的绑定集,令牌轨恒 GLOBAL 且为空数组。
type Actor struct {
	Kind      string   `json:"kind"`
	Name      string   `json:"name"`
	Subject   string   `json:"subject"`
	Scope     string   `json:"scope"`
	TenantIDs []string `json:"tenant_ids"`
}

// AuthorizeScope 管理面范围判定的唯一决策函数:auth(audit(scope(mux))) 的路由级
// 准入与处理器内的资源级判定都经此。未知范围、空绑定与 global-only 对 scoped
// 主体一律拒绝(失败关闭);列表类路由(resourceTenant 为空)放行后由处理器按绑定集过滤。
func AuthorizeScope(actor Actor, class routeClass, resourceTenant string) bool {
	if class == routeExempt {
		return true
	}
	if actor.Scope == "GLOBAL" {
		return true
	}
	if actor.Scope != "TENANT" || len(actor.TenantIDs) == 0 {
		return false
	}
	if class != routeTenantScoped {
		return false
	}
	if resourceTenant == "" {
		return true
	}
	return slices.Contains(actor.TenantIDs, resourceTenant)
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
			actor = Actor{Kind: "federated", Name: p.Username, Subject: p.Subject, Scope: p.Scope}
			// 范围解析随请求读一次绑定;管理面基数极小,不引入缓存层。
			ids, err := store.AdminPrincipalTenants(r.Context(), d.DB, p.Subject)
			if err != nil {
				respondErr(d, w, r, err, "auth_failed")
				return actor, false
			}
			actor.TenantIDs = ids
		} else if !errors.Is(err, auth.ErrAdminCredentials) {
			respondErr(d, w, r, err, "auth_failed")
			return actor, false
		}
	}
	if actor.Kind == "" && d.Token != "" &&
		secureEqual(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), d.Token) {
		actor = Actor{Kind: "token", Name: "management-token", Scope: "GLOBAL", TenantIDs: []string{}}
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
	actor := Actor{Kind: "federated", Name: p.Username, Subject: p.Subject, Scope: p.Scope, TenantIDs: []string{}}
	if ids, err := store.AdminPrincipalTenants(r.Context(), d.DB, p.Subject); err == nil {
		actor.TenantIDs = ids
	}
	return actor
}

// scopeDenial 请求级拒绝原因持有者:scope 中间件与资源处理器写入,
// 审计收尾统一落 denied(含 GET),拒绝可追责且不产生重复行。
type scopeDenial struct {
	reason string
	target string
}

type scopeDenialKey struct{}

func withScopeDenial(r *http.Request) (*http.Request, *scopeDenial) {
	h := &scopeDenial{}
	return r.WithContext(context.WithValue(r.Context(), scopeDenialKey{}, h)), h
}

// markScopeDenied 记录拒绝原因;无持有者(单元直调处理器)时静默跳过。
func markScopeDenied(r *http.Request, reason, target string) {
	if h, _ := r.Context().Value(scopeDenialKey{}).(*scopeDenial); h != nil {
		h.reason, h.target = reason, target
	}
}

// denyScope 路由类准入拒绝:403 + 固定原因码,审计由外层统一落 denied。
func denyScope(w http.ResponseWriter, r *http.Request, actor Actor, class routeClass) {
	reason := "scope_denied"
	switch {
	case actor.Scope != "GLOBAL" && actor.Scope != "TENANT":
		reason = "scope_unknown"
	case actor.Scope == "TENANT" && len(actor.TenantIDs) == 0:
		reason = "scope_unbound"
	case class != routeTenantScoped:
		reason = "scope_global_only"
	}
	markScopeDenied(r, reason, "")
	writeAdminError(w, r, 403, protocol.TypeForbidden, "scope_denied", "管理范围不足")
}

// denyResourceScope 资源级越界拒绝:按不存在处理(404),不披露范围外资源,
// 原因码与目标值进审计 denied。
func denyResourceScope(w http.ResponseWriter, r *http.Request, status int, typ, code, msg, reason, target string) {
	markScopeDenied(r, reason, target)
	writeAdminError(w, r, status, typ, code, msg)
}

// scopeOperators 路由类准入中间件,串在审计内侧:认证只确定身份,
// 能否触达路由由 AuthorizeScope 单点决定;资源级判定复用同一函数。
func scopeOperators(mux *http.ServeMux) http.Handler {
	classes := make(map[string]routeClass, len(adminRouteTable))
	for _, e := range adminRouteTable {
		classes[e.Pattern] = e.effectiveClass()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, pattern := mux.Handler(r)
		r.Pattern = pattern
		class, ok := classes[pattern]
		if !ok {
			// 未匹配路径按 global-only 兜底,scoped 主体不得探测未知路由。
			class = routeGlobalOnly
		}
		if class == routeExempt {
			mux.ServeHTTP(w, r)
			return
		}
		actor := ActorFrom(r.Context())
		if !AuthorizeScope(actor, class, "") {
			denyScope(w, r, actor, class)
			return
		}
		mux.ServeHTTP(w, r)
	})
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

// adminUsersPatch 按 sub 启停主体并授予范围;主体只能由虎符首登产生。
// 省略字段不动,scope/tenant_ids 整包替换;范围授予仅 GLOBAL actor(路由层 global-only)。
// 合并与落库在 store 单事务内完成,并发补丁与跨字段写入不产生假成功或半生效。
func adminUsersPatch(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Disabled  *bool     `json:"disabled"`
			Scope     *string   `json:"scope"`
			TenantIDs *[]string `json:"tenant_ids"`
		}
		if err := decode(r, &in); err != nil ||
			(in.Disabled == nil && in.Scope == nil && in.TenantIDs == nil) {
			writeAdminError(w, r, 400, protocol.TypeBadRequest, "bad_request",
				"须提供 disabled/scope/tenant_ids 之一")
			return
		}
		err := store.PatchAdminPrincipalFields(r.Context(), d.DB, r.PathValue("subject"),
			store.AdminPrincipalPatch{Disabled: in.Disabled, Scope: in.Scope, TenantIDs: in.TenantIDs})
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

// scopeAuditResult scope 拒绝(路由级或资源级)改写结局/目标/detail;
// 非拒绝保持既有 outcome 映射与调用方摘要。
func scopeAuditResult(status int, target, detail string, denial *scopeDenial) (string, string, string) {
	outcome := auditOutcome(status)
	if denial == nil || denial.reason == "" || status < 400 {
		return outcome, target, detail
	}
	if denial.target != "" {
		target = denial.target
	}
	if b, err := json.Marshal(map[string]string{"reason": denial.reason}); err == nil {
		detail = string(b)
	}
	return "denied", target, detail
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
	"scope": true, "tenant_ids": true,
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
	switch typed := v.(type) {
	case string, json.Number, bool, nil:
		return v
	case []any:
		// 仅白名单键能走到这里;字符串数组按序连接,含非字符串元素时整键省略。
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			s, ok := item.(string)
			if !ok {
				return "<omitted>"
			}
			parts = append(parts, s)
		}
		return strings.Join(parts, ",")
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

// auditOperators 管理面审计中间件：非 GET 变更一律留痕；GET 仅在 scope 拒绝时
// 留 denied 行。读体会留下安全摘要，收尾按实际路由与状态落审计行。
func auditOperators(d Deps, mux *http.ServeMux, next http.Handler) http.Handler {
	csrf := http.NewCrossOriginProtection()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, pattern := mux.Handler(r)
		r.Pattern = pattern
		fillPathValues(r, pattern)
		r, denial := withScopeDenial(r)
		actor := ActorFrom(r.Context())
		aw := &auditWriter{ResponseWriter: w}
		if r.Method == http.MethodGet {
			next.ServeHTTP(aw, r)
			if denial.reason != "" && aw.status >= 400 {
				aw.finishAudit(d, r, actor, "{}", denial)
			}
			return
		}
		detail := "{}"
		// 已认证变更与鉴权拒绝共用收尾，读取失败也留下安全摘要。
		defer func() {
			aw.finishAudit(d, r, actor, detail, denial)
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
// scope 拒绝(路由级或资源级)统一 outcome=denied,detail 只留固定原因码与目标值。
func (aw *auditWriter) finishAudit(d Deps, r *http.Request, actor Actor, detail string, denial *scopeDenial) {
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
	outcome, target, detail := scopeAuditResult(status, auditTarget(r), detail, denial)
	action := auditAction(r.Pattern, r.Method)
	a := store.AdminAudit{
		ID:        uuid.NewV7().String(),
		RequestID: logging.RequestIDFrom(r.Context()),
		ActorKind: actor.Kind,
		ActorName: actor.Name,
		Action:    action,
		TargetID:  target,
		Outcome:   outcome,
		Detail:    detail,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if err := store.AppendAdminAudit(r.Context(), d.DB, a); err != nil {
		d.Logger.Error("管理动作审计写入失败", "request_id", a.RequestID, "action", action, "error_detail", err.Error())
	}
}
