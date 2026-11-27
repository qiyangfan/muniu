// Package admin 承载运维面鉴权（Keycloak OIDC 联邦会话/共享令牌）与动作审计；
// 共享令牌、活跃主体与联邦均未配置时整体关闭。本地账密轨已删除，账户权威只在Keycloak。
// API key 管理接口复用 auth 包的签发与校验逻辑。
package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/solvela/muniu/internal/auth"
	"github.com/solvela/muniu/internal/balance"
	"github.com/solvela/muniu/internal/config"
	"github.com/solvela/muniu/internal/health"
	"github.com/solvela/muniu/internal/keypool"
	"github.com/solvela/muniu/internal/logging"
	"github.com/solvela/muniu/internal/metering"
	"github.com/solvela/muniu/internal/oauth"
	"github.com/solvela/muniu/internal/oidc"
	"github.com/solvela/muniu/internal/plugin"
	"github.com/solvela/muniu/internal/protocol"
	"github.com/solvela/muniu/internal/ratelimit"
	"github.com/solvela/muniu/internal/store"
)

// CacheStatsProvider 语义缓存统计源，由装配注入；nil 时统计端点返回空态。
type CacheStatsProvider func() map[string]any

// StaticEntry YAML 静态兜底价目在 GET 列表中的展示形态；时间窗口如实透出，
// 避免控制台编辑 yaml 行时静默丢窗。
type StaticEntry struct {
	Def         metering.Pricing
	Timezone    string
	TimeWindows []PricingWindowView
}

// Deps 管理面依赖。Verifier 用于吊销 key、变更租户后即时清空鉴权缓存；不注入则只能等 TTL 收敛。
// PricingDir 是转发热路径消费的计价目录，StaticPricing 供 GET 列表展示 YAML 兜底价，
// RefreshPricing 由装配层注入，在改价持久化成功后重建目录并原子替换。
// Runtime 为控制面消费侧聚合；Managed 下共享控制本地写入口一律拒绝。
type Deps struct {
	DB         *sql.DB
	AdminAuth  *auth.AdminService
	Token      string
	Providers  []config.Provider
	Pools      map[string]*keypool.Pool
	CacheStats CacheStatsProvider
	Topology   func() ([]config.Provider, map[string]*keypool.Pool)
	Verifier   *auth.Verifier
	Managed    bool
	Runtime    RuntimeProvider
	Logs       func(limit int) []logging.Entry
	// ServiceAPIEnabled 服务只读 API 是否已配置（供状态页诊断）。
	ServiceAPIEnabled bool

	StaticImagePricing     map[string]metering.ImagePrice
	UpdateImagePricing     func(context.Context, string, *store.ImagePricingRecord) error
	PricingDir             *metering.PricingDirectory
	StaticPricing          map[string]StaticEntry
	RefreshPricing         func() error
	Plugins                *plugin.Registry   // WASM 插件注册表；nil = 端点返回禁用态
	Balance                *balance.Service   // 余额观测服务;nil = 观测未启用,端点返回禁用/空态
	Health                 *health.Manager    // 上游健康管理器;nil = 观测未启用,端点返回空态
	AlertWebhookConfigured bool               // webhook_url 是否配置;false 时投递自监控端点返回关闭态
	OAuthFlow              *oauth.Flow        // OAuth 授权流;nil = oauth 段未声明,授权端点返回关闭态(账号 CRUD 不依赖)
	Federated              *oidc.RelyingParty // Keycloak OIDC 联邦;nil = admin_oidc 未声明,联邦入口返回关闭态
	PostLogoutRedirect     string             // Keycloak登出后回跳;空则按 redirect_url 同源推导 /console/
	SecureCookies          bool               // 管理面 cookie 是否带 Secure(反代终止 TLS 时必须显式开启)
	LoginLimiter           *ratelimit.Limiter // 登录发起/回调按 IP 限流;nil 由 New 装配进程内实现

	Logger *slog.Logger
}

// routeClass 管理面路由的范围分类。零值是漏分类哨兵:执行期按 global-only 兜底
// (对 scoped 主体拒绝),分类断言测试要求逐条显式标注,新增路由无法静默放行。
type routeClass int

const (
	routeUnclassified routeClass = iota
	routeExempt                  // 认证入口与 whoami/logout:主题未确定前也必须可达
	routeGlobalOnly              // 全局运行面:仅 GLOBAL actor
	routeTenantScoped            // 租户资源面:按绑定集收敛,资源级判定在处理器内
)

// adminRouteEntry 管理面路由表条目。
type adminRouteEntry struct {
	Pattern string
	Class   routeClass
	Handler func(Deps) http.HandlerFunc
}

// effectiveClass 漏分类按 global-only 执行,与"未分类默认拒绝 scoped"的失败关闭一致。
func (e adminRouteEntry) effectiveClass() routeClass {
	if e.Class == routeUnclassified {
		return routeGlobalOnly
	}
	return e.Class
}

// adminRouteTable 管理面路由单一事实源：New 据此注册，Routes() 据此回报，漂移门禁消费同一份。
// 两处手写必然漂移，模式只在这里声明一次。
var adminRouteTable = []adminRouteEntry{
	{"POST /admin/v1/auth/logout", routeExempt, adminLogout},
	{"POST /admin/v1/auth/logout/sso", routeExempt, adminLogoutSSO},
	{"GET /admin/v1/auth/whoami", routeExempt, adminWhoami},
	{"GET /admin/v1/auth/federation", routeExempt, adminFederationInfo},
	{"GET /admin/v1/auth/oidc/start", routeExempt, adminFederatedStart},
	{"GET /admin/v1/auth/oidc/callback", routeExempt, adminFederatedCallback},
	{"GET /admin/v1/admins", routeGlobalOnly, adminUsersList},
	{"PATCH /admin/v1/admins/{subject}", routeGlobalOnly, adminUsersPatch},
	{"GET /admin/v1/audit", routeGlobalOnly, adminAuditList},
	{"GET /admin/v1/image-pricing", routeGlobalOnly, imagePricingList},
	{"PUT /admin/v1/image-pricing/{alias}", routeGlobalOnly, imagePricingPut},
	{"DELETE /admin/v1/image-pricing/{alias}", routeGlobalOnly, imagePricingDelete},
	{"GET /admin/v1/tenants", routeTenantScoped, listTenants},
	{"POST /admin/v1/tenants", routeGlobalOnly, createTenant},
	{"PATCH /admin/v1/tenants/{id}", routeGlobalOnly, patchTenant},
	{"GET /admin/v1/keys", routeTenantScoped, listKeys},
	{"POST /admin/v1/keys", routeTenantScoped, createKey},
	{"PATCH /admin/v1/keys/{id}", routeTenantScoped, patchKey},
	{"DELETE /admin/v1/keys/{prefix}", routeTenantScoped, revokeKey},
	{"GET /admin/v1/usage", routeTenantScoped, usage},
	{"GET /admin/v1/providers", routeGlobalOnly, providers},
	{"GET /admin/v1/provider-presets", routeGlobalOnly, providerPresets},
	{"GET /admin/v1/cache/stats", routeGlobalOnly, cacheStats},
	{"GET /admin/v1/pricing", routeGlobalOnly, pricingList},
	{"PUT /admin/v1/pricing/{alias}", routeGlobalOnly, pricingPut},
	{"DELETE /admin/v1/pricing/{alias}", routeGlobalOnly, pricingDelete},
	{"GET /admin/v1/plugins", routeGlobalOnly, pluginsList},
	{"PUT /admin/v1/plugins/{name}", routeGlobalOnly, pluginPut},
	{"PATCH /admin/v1/plugins/{name}", routeGlobalOnly, pluginPatch},
	{"GET /admin/v1/balances", routeGlobalOnly, balancesOverview},
	{"POST /admin/v1/balances/{provider}/manual", routeGlobalOnly, balanceManual},
	{"POST /admin/v1/balances/{provider}/refresh", routeGlobalOnly, balanceRefresh},
	{"GET /admin/v1/oauth/accounts", routeGlobalOnly, oauthAccountsList},
	{"POST /admin/v1/oauth/accounts", routeGlobalOnly, oauthAccountsCreate},
	{"DELETE /admin/v1/oauth/accounts/{id}", routeGlobalOnly, oauthAccountDelete},
	{"POST /admin/v1/oauth/authorize", routeGlobalOnly, oauthAuthorize},
	{"GET /admin/v1/oauth/callback", routeExempt, oauthCallback},
	{"GET /admin/v1/alerts/delivery-status", routeGlobalOnly, alertDeliveryStatus},
	{"GET /admin/v1/upstream-health", routeGlobalOnly, upstreamHealth},
	{"GET /admin/v1/runtime/status", routeGlobalOnly, runtimeStatus},
	{"GET /admin/v1/runtime/shared-control", routeGlobalOnly, runtimeSharedControl},
	{"GET /admin/v1/runtime/commands", routeGlobalOnly, runtimeCommands},
	{"POST /admin/v1/runtime/commands", routeGlobalOnly, runtimeCommandExecute},
	{"GET /admin/v1/runtime/logs", routeGlobalOnly, runtimeLogs},
}

// New 按共享令牌、活跃主体或Keycloak联邦开放管理面，认证装配失败不暴露空壳。
func New(ctx context.Context, d Deps) http.Handler {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.Token == "" && d.Federated == nil {
		active, err := store.HasActiveAdminPrincipals(ctx, d.DB)
		if err != nil {
			d.Logger.Error("管理员挂载条件查询失败", "error_detail", err.Error())
			return nil
		}
		if !active {
			return nil
		}
	}
	if d.AdminAuth == nil {
		svc, err := auth.NewAdminService(ctx, d.DB)
		if err != nil {
			d.Logger.Error("管理员认证装配失败", "error_detail", err.Error())
			return nil
		}
		d.AdminAuth = svc
	}
	initLoginLimiter(&d)
	mux := http.NewServeMux()
	for _, e := range adminRouteTable {
		mux.Handle(e.Pattern, e.Handler(d))
	}
	return authOperators(d, auditOperators(d, mux, scopeOperators(mux)))
}

// initLoginLimiter 联邦入口默认进程内每 IP 限流;装配层可注入共享/测试实现。
func initLoginLimiter(d *Deps) {
	if d.Federated != nil && d.LoginLimiter == nil {
		d.LoginLimiter = ratelimit.New()
	}
}

// secureEqual 恒定时间字符串比较。
func secureEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := 0; i < len(a); i++ {
		v |= a[i] ^ b[i]
	}
	return v == 0
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeAdminError(w http.ResponseWriter, r *http.Request, status int, typ, code, msg string) {
	writeJSON(w, status, protocol.NewErrorEnvelope(typ, code, msg, logging.RequestIDFrom(r.Context())))
}

// respondErr 业务错误返回 400 并透出原始 message；内部故障收敛为 500 统一「内部错误」，
// 细节只进日志，数据库驱动错误串不外泄。
func respondErr(d Deps, w http.ResponseWriter, r *http.Request, err error, bizCode string) {
	if store.IsBusiness(err) {
		writeAdminError(w, r, 400, protocol.TypeBadRequest, bizCode, err.Error())
		return
	}
	d.Logger.Error("管理面操作失败", "request_id", logging.RequestIDFrom(r.Context()), "error_detail", err.Error())
	writeAdminError(w, r, 500, protocol.TypeInternal, "internal", "内部错误")
}

func decode(r *http.Request, v any) error {
	defer func() { _ = r.Body.Close() }()
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(v); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("请求体必须是单个 JSON 值")
	}
	return nil
}

// ---- tenants ----

// rejectManaged managed 下共享控制本地只读：价目/租户/拓扑写入口一律拒绝（第二写者闸）。
func rejectManaged(d Deps, w http.ResponseWriter, r *http.Request) bool {
	if !d.Managed {
		return false
	}
	writeAdminError(w, r, 403, protocol.TypeForbidden, "shared_control_read_only",
		"managed 模式下共享控制只读,请由锦官修改")
	return true
}

func listTenants(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor := ActorFrom(r.Context())
		var (
			tenants []store.Tenant
			err     error
		)
		if actor.Scope == "GLOBAL" {
			tenants, err = store.ListTenants(r.Context(), d.DB)
		} else {
			tenants, err = store.ListTenantsByIDs(r.Context(), d.DB, actor.TenantIDs)
		}
		if err != nil {
			// 内部错误细节只进日志，对外统一归一 message
			d.Logger.Error("列出租户失败", "request_id", logging.RequestIDFrom(r.Context()), "error_detail", err.Error())
			writeAdminError(w, r, 500, protocol.TypeInternal, "internal", "内部错误")
			return
		}
		writeJSON(w, 200, map[string]any{"tenants": tenants})
	}
}

func createTenant(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if rejectManaged(d, w, r) {
			return
		}
		var req struct {
			Name string `json:"name"`
		}
		if err := decode(r, &req); err != nil || strings.TrimSpace(req.Name) == "" {
			writeAdminError(w, r, 400, protocol.TypeBadRequest, "bad_request", "name 必填")
			return
		}
		id, err := store.CreateTenant(r.Context(), d.DB, strings.TrimSpace(req.Name))
		if err != nil {
			respondErr(d, w, r, err, "tenant_create_failed")
			return
		}
		writeJSON(w, 200, map[string]any{"id": id, "name": strings.TrimSpace(req.Name)})
	}
}

func patchTenant(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if rejectManaged(d, w, r) {
			return
		}
		id := r.PathValue("id")
		var req struct {
			Status              *string `json:"status"`
			DailyQuotaCredits   *int64  `json:"daily_quota_credits"`
			TPMLimit            *int64  `json:"tpm_limit"`
			SemanticCacheEnable *bool   `json:"semantic_cache_enabled"`
		}
		if err := decode(r, &req); err != nil {
			writeAdminError(w, r, 400, protocol.TypeBadRequest, "bad_request", "请求体非法")
			return
		}
		// 负额度会被 quota/tpm 判定解释为「不限」，与收紧意图相反，入口即拒
		if (req.DailyQuotaCredits != nil && *req.DailyQuotaCredits < 0) ||
			(req.TPMLimit != nil && *req.TPMLimit < 0) {
			writeAdminError(w, r, 400, protocol.TypeBadRequest, "bad_request", "daily_quota_credits/tpm_limit 不可为负")
			return
		}
		ok, err := store.UpdateTenant(r.Context(), d.DB, id, store.UpdateTenantPatch{
			Status:              derefStr(req.Status),
			DailyQuotaCredits:   req.DailyQuotaCredits,
			TPMLimit:            req.TPMLimit,
			SemanticCacheEnable: req.SemanticCacheEnable,
		})
		if err != nil {
			respondErr(d, w, r, err, "bad_request")
			return
		}
		if !ok {
			writeAdminError(w, r, 404, protocol.TypeBadRequest, "tenant_not_found", "租户不存在")
			return
		}
		if d.Verifier != nil {
			d.Verifier.Invalidate() // 租户禁用/额度变更即时生效
		}
		writeJSON(w, 200, map[string]any{"ok": true})
	}
}

// ---- keys ----

func listKeys(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor := ActorFrom(r.Context())
		var (
			keys []auth.KeyRecord
			err  error
		)
		if actor.Scope == "GLOBAL" {
			keys, err = auth.ListKeys(r.Context(), d.DB)
		} else {
			keys, err = auth.ListKeysByTenants(r.Context(), d.DB, actor.TenantIDs)
		}
		if err != nil {
			d.Logger.Error("列出 key 失败", "request_id", logging.RequestIDFrom(r.Context()), "error_detail", err.Error())
			writeAdminError(w, r, 500, protocol.TypeInternal, "internal", "内部错误")
			return
		}
		if keys == nil {
			keys = []auth.KeyRecord{}
		}
		writeJSON(w, 200, map[string]any{"keys": keys})
	}
}

// managedCreateKeyTenant managed 签发前的目录闸：快照就绪且租户 active，否则已写信封。
func managedCreateKeyTenant(d Deps, w http.ResponseWriter, r *http.Request, tenantName string) bool {
	// 目录权威在控制面：从未获得有效快照/last-known 时禁止签发，避免绑定本地种子租户。
	if d.Runtime == nil || !d.Runtime.SnapshotStatus().Ready {
		writeAdminError(w, r, 503, protocol.TypeUnavailable, "tenant_directory_unavailable",
			"控制面租户目录尚未就绪,暂不可签发 key")
		return false
	}
	return managedTenantActive(d, w, r, tenantName)
}

// managedTenantActive managed 下 key 绑定租户必须命中快照目录且为 active。
func managedTenantActive(d Deps, w http.ResponseWriter, r *http.Request, tenantName string) bool {
	status, err := store.TenantStatusByName(r.Context(), d.DB, tenantName)
	if err != nil || status != "active" {
		writeAdminError(w, r, 400, protocol.TypeBadRequest, "tenant_unavailable",
			"租户不在控制面租户目录或已禁用")
		return false
	}
	return true
}

// createKeyRequest 签发请求体。
type createKeyRequest struct {
	Tenant                string   `json:"tenant"`
	Name                  string   `json:"name"`
	RPMLimit              int64    `json:"rpm_limit"`
	TPMLimit              int64    `json:"tpm_limit"`
	DailyQuotaCredits     int64    `json:"daily_quota_credits"`
	MaxRequestCostCredits int64    `json:"max_request_cost_credits"`
	AllowedModels         []string `json:"allowed_models"` // 缺省或空 = 不限；空白条目整单拒绝
	ExpiresAt             string   `json:"expires_at"`     // 可空，RFC3339；空 = 永不过期
}

func createKey(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		req, opts, tenantName, ok := parseCreateKey(d, w, r)
		if !ok {
			return
		}
		plaintext, rec, err := auth.CreateKey(r.Context(), d.DB, tenantName, req.Name, req.RPMLimit, opts)
		if err != nil {
			respondErr(d, w, r, err, "key_create_failed")
			return
		}
		writeJSON(w, 200, map[string]any{
			"key": plaintext, // 管理面明文仅此一次，与 CLI 同语义
			"id":  rec.ID, "prefix": rec.Prefix, "tenant": rec.TenantID,
			"rpm_limit": rec.RPMLimit, "tpm_limit": rec.TPMLimit,
			"daily_quota_credits": rec.DailyQuotaCredits, "expires_at": rec.ExpiresAt,
			"allowed_models": rec.AllowedModels,
		})
	}
}

// parseCreateKey 解析并校验签发请求；失败已写信封。
func parseCreateKey(d Deps, w http.ResponseWriter, r *http.Request) (
	createKeyRequest, auth.KeyOpts, string, bool,
) {
	var req createKeyRequest
	if err := decode(r, &req); err != nil || req.Name == "" {
		writeAdminError(w, r, 400, protocol.TypeBadRequest, "bad_request", "name 必填")
		return req, auth.KeyOpts{}, "", false
	}
	tenantName := strings.TrimSpace(req.Tenant)
	if tenantName == "" {
		tenantName = "default"
	}
	if !authorizeTenantName(d, w, r, tenantName) {
		return req, auth.KeyOpts{}, "", false
	}
	if d.Managed && !managedCreateKeyTenant(d, w, r, tenantName) {
		return req, auth.KeyOpts{}, "", false
	}
	if req.MaxRequestCostCredits < 0 {
		writeAdminError(w, r, 400, protocol.TypeBadRequest, "bad_request", "max_request_cost_credits 不可为负")
		return req, auth.KeyOpts{}, "", false
	}
	opts := auth.KeyOpts{
		TPMLimit: req.TPMLimit, DailyQuotaCredits: req.DailyQuotaCredits,
		MaxRequestCostCredits: req.MaxRequestCostCredits, AllowedModels: req.AllowedModels,
	}
	if !applyKeyExpiry(w, r, req.ExpiresAt, &opts) {
		return req, auth.KeyOpts{}, "", false
	}
	return req, opts, tenantName, true
}

// applyKeyExpiry 解析创建期过期时刻；为空即永不过期，过去时刻拒绝。
func applyKeyExpiry(w http.ResponseWriter, r *http.Request, raw string, opts *auth.KeyOpts) bool {
	if raw == "" {
		return true
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		writeAdminError(w, r, 400, protocol.TypeBadRequest, "bad_request", "expires_at 须为 RFC3339 形态")
		return false
	}
	if t.Before(time.Now()) {
		writeAdminError(w, r, 400, protocol.TypeBadRequest, "bad_request",
			"expires_at 不可早于当前时刻(立即失效请经 PATCH 置过去时刻)")
		return false
	}
	opts.ExpiresAt = &t
	return true
}

func applyAllowModels(raw json.RawMessage, patch *auth.LimitsPatch) (string, bool) {
	switch {
	case len(raw) == 0: // 字段缺省：不动
		return "", true
	case string(raw) == "null":
		patch.ClearAllowModels = true
		return "", true
	default:
		var models []string
		if err := json.Unmarshal(raw, &models); err != nil {
			// 空白条目整单拒绝在 auth 归一化，返回 400
			return "allowed_models 须为字符串数组或 null", false
		}
		patch.AllowedModels = models
		return "", true
	}
}

// applyExpiresAt 把过期时刻字段折进补丁：缺省不动、null 清除、RFC3339 字符串设定。
func applyExpiresAt(raw json.RawMessage, patch *auth.LimitsPatch) (string, bool) {
	switch {
	case len(raw) == 0: // 字段缺省：不动
		return "", true
	case string(raw) == "null":
		patch.ClearExpires = true
		return "", true
	default:
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return "expires_at 须为 RFC3339 字符串或 null", false
		}
		t, perr := time.Parse(time.RFC3339, s)
		if perr != nil {
			return "expires_at 须为 RFC3339 形态", false
		}
		patch.ExpiresAt = &t
		return "", true
	}
}

type keyPatchRequest struct {
	RPMLimit              *int64          `json:"rpm_limit"`
	TPMLimit              *int64          `json:"tpm_limit"`
	DailyQuotaCredits     *int64          `json:"daily_quota_credits"`
	ExpiresAt             json.RawMessage `json:"expires_at"`               // 缺省=不动；null=清除；"RFC3339"=设定
	Disabled              *bool           `json:"disabled"`                 // 缺省=不动；true=停用，false=恢复
	MaxRequestCostCredits *int64          `json:"max_request_cost_credits"` // 缺省=不动；0=不限
	AllowedModels         json.RawMessage `json:"allowed_models"`           // 缺省=不动；null=清除即不限；数组=整体设定
}

// patchKey 局部更新 key 运行态限额：省略字段不动，expires_at 显式 null 表示清除。
// 写后 Invalidate：限额是 DB 可变状态，须走吊销同款失效协议，下一请求即新限额。
func patchKey(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authorizeKeyID(d, w, r, r.PathValue("id")) {
			return
		}
		var req keyPatchRequest
		if err := decode(r, &req); err != nil {
			writeAdminError(w, r, 400, protocol.TypeBadRequest, "bad_request", "请求体非法")
			return
		}
		if req.MaxRequestCostCredits != nil && *req.MaxRequestCostCredits < 0 {
			writeAdminError(w, r, 400, protocol.TypeBadRequest, "bad_request", "max_request_cost_credits 不可为负")
			return
		}
		patch := auth.LimitsPatch{
			RPMLimit: req.RPMLimit, TPMLimit: req.TPMLimit, DailyQuotaCredits: req.DailyQuotaCredits,
			Disabled: req.Disabled, MaxRequestCostCredits: req.MaxRequestCostCredits,
		}
		if msg, ok := applyAllowModels(req.AllowedModels, &patch); !ok {
			writeAdminError(w, r, 400, protocol.TypeBadRequest, "bad_request", msg)
			return
		}
		if msg, ok := applyExpiresAt(req.ExpiresAt, &patch); !ok {
			writeAdminError(w, r, 400, protocol.TypeBadRequest, "bad_request", msg)
			return
		}
		rec, err := auth.UpdateKeyLimits(r.Context(), d.DB, r.PathValue("id"), patch)
		if errors.Is(err, store.ErrNotFound) {
			writeAdminError(w, r, http.StatusNotFound, protocol.TypeNotFound, "key_not_found", "key 不存在")
			return
		}
		if err != nil {
			respondErr(d, w, r, err, "key_patch_failed")
			return
		}
		if d.Verifier != nil {
			d.Verifier.Invalidate() // 新限额下一请求即生效，不等缓存 TTL
		}
		writeJSON(w, 200, rec)
	}
}

func revokeKey(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		prefix := r.PathValue("prefix")
		if !authorizeKeyPrefix(d, w, r, prefix) {
			return
		}
		n, err := auth.RevokeKeyByPrefix(r.Context(), d.DB, prefix)
		if err != nil {
			d.Logger.Error("吊销 key 失败", "request_id", logging.RequestIDFrom(r.Context()), "error_detail", err.Error())
			writeAdminError(w, r, 500, protocol.TypeInternal, "internal", "内部错误")
			return
		}
		if n == 0 {
			writeAdminError(w, r, 404, protocol.TypeBadRequest, "key_not_found", "无 active 状态的该 prefix")
			return
		}
		if d.Verifier != nil {
			d.Verifier.Invalidate() // 吊销即时生效，不等缓存 TTL
		}
		writeJSON(w, 200, map[string]any{"revoked": n})
	}
}

// ---- usage 聚合 ----

// parseUsageTS 解析可选 RFC3339 时间参数：空值返回空串交由调用方补缺省；
// 非法时写出 400 并返回 false。
func parseUsageTS(w http.ResponseWriter, r *http.Request, v, label string) (string, bool) {
	if v == "" {
		return "", true
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		writeAdminError(w, r, 400, protocol.TypeBadRequest, "bad_request", label+" 须为 RFC3339 形态")
		return "", false
	}
	// 必须归一为 UTC：usage_logs.created_at 存 Z 后缀形态，SQL 按字典序比较，
	// 带时区偏移的入参不归一会让区间过滤整体错位
	return t.UTC().Format(time.RFC3339), true
}

// usageRange 解析 from/to 区间，缺省为最近 7 天至当前时刻。
func usageRange(w http.ResponseWriter, r *http.Request, now time.Time) (from, to string, ok bool) {
	q := r.URL.Query()
	from, ok = parseUsageTS(w, r, q.Get("from"), "from")
	if !ok {
		return "", "", false
	}
	if from == "" {
		from = now.Add(-7 * 24 * time.Hour).Format(time.RFC3339)
	}
	to, ok = parseUsageTS(w, r, q.Get("to"), "to")
	if !ok {
		return "", "", false
	}
	if to == "" {
		to = now.Format(time.RFC3339)
	}
	if from > to {
		writeAdminError(w, r, 400, protocol.TypeBadRequest, "bad_request", "from 不可晚于 to")
		return "", "", false
	}
	return from, to, true
}

func usage(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		groupBy := q.Get("group_by")
		switch groupBy {
		case "day", "key", "model", "provider":
		default:
			writeAdminError(w, r, 400, protocol.TypeBadRequest, "bad_request", "group_by 须为 day|key|model|provider")
			return
		}
		from, to, ok := usageRange(w, r, time.Now().UTC())
		if !ok {
			return
		}
		actor := ActorFrom(r.Context())
		tenantParam := q.Get("tenant_id")
		var (
			pts []store.UsagePoint
			err error
		)
		if actor.Scope == "GLOBAL" {
			pts, err = store.UsageAggregate(r.Context(), d.DB, store.UsageDim(groupBy), from, to,
				tenantParam, q.Get("key_id"))
		} else {
			// 显式参数必须是绑定子集;越界按不存在处理,缺省取绑定并集一次 IN 聚合。
			if tenantParam != "" && !AuthorizeScope(actor, routeTenantScoped, tenantParam) {
				denyResourceScope(w, r, http.StatusNotFound, protocol.TypeNotFound,
					"not_found", "资源不存在", "tenant_out_of_scope", tenantParam)
				return
			}
			scopeIDs := actor.TenantIDs
			if tenantParam != "" {
				scopeIDs = []string{tenantParam}
			}
			pts, err = store.UsageAggregateByTenants(r.Context(), d.DB, store.UsageDim(groupBy), from, to,
				scopeIDs, q.Get("key_id"))
		}
		if err != nil {
			respondErr(d, w, r, err, "bad_request")
			return
		}
		if pts == nil {
			pts = []store.UsagePoint{}
		}
		writeJSON(w, 200, map[string]any{"points": pts, "from": from, "to": to, "group_by": groupBy})
	}
}

// ---- providers 拓扑 + key 池实时状态 ----

func providers(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		now := time.Now()
		providers, pools := d.Providers, d.Pools
		if d.Topology != nil {
			providers, pools = d.Topology()
		}
		out := make([]map[string]any, 0, len(providers))
		for _, p := range providers {
			keys := make([]map[string]any, 0, len(p.Keys))
			for _, kr := range p.Keys {
				// 成员键与装配规则同源(env 优先,account 引用渠道用账号 ID):oauth 成员
				// 以 kr.Env 作键恒为空串/unknown,渠道不可观测(评审域二 P2)
				id := kr.Env
				if id == "" {
					id = kr.Account
				}
				st := "unknown"
				if pool := pools[p.Name]; pool != nil {
					st = pool.StateOf(id, now)
				}
				keys = append(keys, map[string]any{"id": id, "weight": kr.Weight, "state": st})
			}
			out = append(out, map[string]any{
				"name": p.Name, "adapter": p.Adapter, "base_url": p.BaseURL,
				"weight": p.Weight, "priority": p.Priority, "keys": keys,
			})
		}
		writeJSON(w, 200, map[string]any{"providers": out})
	}
}

// ---- 缓存统计 ----

func cacheStats(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.CacheStats == nil {
			writeJSON(w, 200, map[string]any{"enabled": false})
			return
		}
		stats := d.CacheStats()
		stats["enabled"] = true
		writeJSON(w, 200, stats)
	}
}

// derefStr 指针转值，nil 转空串；UpdateTenantPatch 以空串表达「不动」。
func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// Routes 管理面注册的路由模式表，由 adminRouteTable 派生，顺序稳定；漂移门禁与装配层共享。
func Routes() []string {
	out := make([]string, 0, len(adminRouteTable))
	for _, e := range adminRouteTable {
		out = append(out, e.Pattern)
	}
	return out
}

// authorizeTenantName TENANT actor 的签发租户判定:名字解析为 id 后必须在绑定集内;
// 不存在与范围外统一 404,不披露范围外租户存在性。GLOBAL 不受约束(保持既有 400 语义)。
func authorizeTenantName(d Deps, w http.ResponseWriter, r *http.Request, name string) bool {
	actor := ActorFrom(r.Context())
	if actor.Scope == "GLOBAL" {
		return true
	}
	id, err := store.TenantIDByName(r.Context(), d.DB, name)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			denyResourceScope(w, r, http.StatusNotFound, protocol.TypeNotFound,
				"tenant_not_found", "租户不存在", "tenant_out_of_scope", name)
			return false
		}
		respondErr(d, w, r, err, "bad_request")
		return false
	}
	if !AuthorizeScope(actor, routeTenantScoped, id) {
		denyResourceScope(w, r, http.StatusNotFound, protocol.TypeNotFound,
			"tenant_not_found", "租户不存在", "tenant_out_of_scope", id)
		return false
	}
	return true
}

// authorizeKeyID TENANT actor 的按 id 操作判定:目标 key 必须属于绑定租户;
// 不存在与越界统一 404。GLOBAL 不做额外查询(零回归)。
func authorizeKeyID(d Deps, w http.ResponseWriter, r *http.Request, id string) bool {
	actor := ActorFrom(r.Context())
	if actor.Scope == "GLOBAL" {
		return true
	}
	tenantID, err := store.APIKeyTenantByID(r.Context(), d.DB, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeAdminError(w, r, http.StatusNotFound, protocol.TypeNotFound, "key_not_found", "key 不存在")
			return false
		}
		respondErr(d, w, r, err, "bad_request")
		return false
	}
	if !AuthorizeScope(actor, routeTenantScoped, tenantID) {
		denyResourceScope(w, r, http.StatusNotFound, protocol.TypeNotFound,
			"not_found", "资源不存在", "tenant_out_of_scope", tenantID)
		return false
	}
	return true
}

// authorizeKeyPrefix TENANT actor 的吊销判定:prefix 命中的所有 active key
// 必须都在绑定集内,任一越界整体拒绝且不写库。
func authorizeKeyPrefix(d Deps, w http.ResponseWriter, r *http.Request, prefix string) bool {
	actor := ActorFrom(r.Context())
	if actor.Scope == "GLOBAL" {
		return true
	}
	tenants, err := store.ActiveAPIKeyTenantsByPrefix(r.Context(), d.DB, prefix)
	if err != nil {
		respondErr(d, w, r, err, "bad_request")
		return false
	}
	if len(tenants) == 0 {
		writeAdminError(w, r, http.StatusNotFound, protocol.TypeNotFound, "key_not_found", "key 不存在")
		return false
	}
	for _, tenantID := range tenants {
		if !AuthorizeScope(actor, routeTenantScoped, tenantID) {
			denyResourceScope(w, r, http.StatusNotFound, protocol.TypeNotFound,
				"not_found", "资源不存在", "tenant_out_of_scope", tenantID)
			return false
		}
	}
	return true
}
