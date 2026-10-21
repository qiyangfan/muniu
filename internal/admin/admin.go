// Package admin 承载运维面双轨鉴权与动作审计；账号和共享令牌均未配置时整体关闭。
// API key 管理接口复用 auth 包的签发与校验逻辑。
package admin

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/solvela/muniu/internal/auth"
	"github.com/solvela/muniu/internal/config"
	"github.com/solvela/muniu/internal/keypool"
	"github.com/solvela/muniu/internal/logging"
	"github.com/solvela/muniu/internal/metering"
	"github.com/solvela/muniu/internal/plugin"
	"github.com/solvela/muniu/internal/protocol"
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
type Deps struct {
	DB         *sql.DB
	AdminAuth  *auth.AdminService
	Token      string
	Providers  []config.Provider
	Pools      map[string]*keypool.Pool
	CacheStats CacheStatsProvider
	Topology   func() ([]config.Provider, map[string]*keypool.Pool)
	Verifier   *auth.Verifier

	StaticImagePricing map[string]metering.ImagePrice
	UpdateImagePricing func(string, *store.ImagePricingRecord) error
	PricingDir         *metering.PricingDirectory
	StaticPricing      map[string]StaticEntry
	RefreshPricing     func() error
	Plugins            *plugin.Registry // WASM 插件注册表；nil = 端点返回禁用态

	Logger *slog.Logger
}

// adminRouteEntry 管理面路由表条目。
type adminRouteEntry struct {
	Pattern string
	Handler func(Deps) http.HandlerFunc
}

// adminRouteTable 管理面路由单一事实源：New 据此注册，Routes() 据此回报，漂移门禁消费同一份。
// 两处手写必然漂移，模式只在这里声明一次。
var adminRouteTable = []adminRouteEntry{
	{"POST /admin/v1/auth/login", adminLogin},
	{"POST /admin/v1/auth/logout", adminLogout},
	{"GET /admin/v1/auth/whoami", adminWhoami},
	{"GET /admin/v1/admins", adminUsersList},
	{"POST /admin/v1/admins", adminUsersCreate},
	{"PATCH /admin/v1/admins/{id}", adminUsersPatch},
	{"GET /admin/v1/audit", adminAuditList},
	{"GET /admin/v1/image-pricing", imagePricingList},
	{"PUT /admin/v1/image-pricing/{alias}", imagePricingPut},
	{"DELETE /admin/v1/image-pricing/{alias}", imagePricingDelete},
	{"GET /admin/v1/tenants", listTenants},
	{"POST /admin/v1/tenants", createTenant},
	{"PATCH /admin/v1/tenants/{id}", patchTenant},
	{"GET /admin/v1/keys", listKeys},
	{"POST /admin/v1/keys", createKey},
	{"PATCH /admin/v1/keys/{id}", patchKey},
	{"DELETE /admin/v1/keys/{prefix}", revokeKey},
	{"GET /admin/v1/usage", usage},
	{"GET /admin/v1/providers", providers},
	{"GET /admin/v1/provider-presets", providerPresets},
	{"GET /admin/v1/cache/stats", cacheStats},
	{"GET /admin/v1/pricing", pricingList},
	{"PUT /admin/v1/pricing/{alias}", pricingPut},
	{"DELETE /admin/v1/pricing/{alias}", pricingDelete},
	{"GET /admin/v1/plugins", pluginsList},
	{"PUT /admin/v1/plugins/{name}", pluginPut},
	{"PATCH /admin/v1/plugins/{name}", pluginPatch},
}

// New 按共享令牌或活跃账号开放管理面，认证装配失败不暴露空壳。
func New(d Deps) http.Handler {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.Token == "" {
		active, err := store.HasActiveAdmins(d.DB)
		if err != nil {
			d.Logger.Error("管理员挂载条件查询失败", "error_detail", err.Error())
			return nil
		}
		if !active {
			return nil
		}
	}
	if d.AdminAuth == nil {
		svc, err := auth.NewAdminService(d.DB)
		if err != nil {
			d.Logger.Error("管理员认证装配失败", "error_detail", err.Error())
			return nil
		}
		d.AdminAuth = svc
	}
	mux := http.NewServeMux()
	for _, e := range adminRouteTable {
		mux.Handle(e.Pattern, e.Handler(d))
	}
	return authOperators(d, auditOperators(d, mux))
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

func listTenants(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tenants, err := store.ListTenants(d.DB)
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
		var req struct {
			Name string `json:"name"`
		}
		if err := decode(r, &req); err != nil || strings.TrimSpace(req.Name) == "" {
			writeAdminError(w, r, 400, protocol.TypeBadRequest, "bad_request", "name 必填")
			return
		}
		id, err := store.CreateTenant(d.DB, strings.TrimSpace(req.Name))
		if err != nil {
			respondErr(d, w, r, err, "tenant_create_failed")
			return
		}
		writeJSON(w, 200, map[string]any{"id": id, "name": strings.TrimSpace(req.Name)})
	}
}

func patchTenant(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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
		ok, err := store.UpdateTenant(d.DB, id, store.UpdateTenantPatch{
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
		keys, err := auth.ListKeys(d.DB)
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

func createKey(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Tenant                string   `json:"tenant"`
			Name                  string   `json:"name"`
			RPMLimit              int64    `json:"rpm_limit"`
			TPMLimit              int64    `json:"tpm_limit"`
			DailyQuotaCredits     int64    `json:"daily_quota_credits"`
			MaxRequestCostCredits int64    `json:"max_request_cost_credits"`
			AllowedModels         []string `json:"allowed_models"` // 缺省或空 = 不限；空白条目整单拒绝
			ExpiresAt             string   `json:"expires_at"`     // 可空，RFC3339；空 = 永不过期
		}
		if err := decode(r, &req); err != nil || req.Name == "" {
			writeAdminError(w, r, 400, protocol.TypeBadRequest, "bad_request", "name 必填")
			return
		}
		if req.MaxRequestCostCredits < 0 {
			writeAdminError(w, r, 400, protocol.TypeBadRequest, "bad_request", "max_request_cost_credits 不可为负")
			return
		}
		opts := auth.KeyOpts{
			TPMLimit: req.TPMLimit, DailyQuotaCredits: req.DailyQuotaCredits,
			MaxRequestCostCredits: req.MaxRequestCostCredits, AllowedModels: req.AllowedModels,
		}
		if req.ExpiresAt != "" {
			t, err := time.Parse(time.RFC3339, req.ExpiresAt)
			if err != nil {
				writeAdminError(w, r, 400, protocol.TypeBadRequest, "bad_request", "expires_at 须为 RFC3339 形态")
				return
			}
			// 创建期拒绝过去时刻：静默接受只会产出看似正常、实际已过期的 key，
			// 立即失效语义归 PATCH
			if t.Before(time.Now()) {
				writeAdminError(w, r, 400, protocol.TypeBadRequest, "bad_request",
					"expires_at 不可早于当前时刻(立即失效请经 PATCH 置过去时刻)")
				return
			}
			opts.ExpiresAt = &t
		}
		plaintext, rec, err := auth.CreateKey(d.DB, req.Tenant, req.Name, req.RPMLimit, opts)
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

// patchKey 局部更新 key 运行态限额：省略字段不动，expires_at 显式 null 表示清除。
// 写后 Invalidate：限额是 DB 可变状态，须走吊销同款失效协议，下一请求即新限额。
func patchKey(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			RPMLimit              *int64          `json:"rpm_limit"`
			TPMLimit              *int64          `json:"tpm_limit"`
			DailyQuotaCredits     *int64          `json:"daily_quota_credits"`
			ExpiresAt             json.RawMessage `json:"expires_at"`               // 缺省=不动；null=清除；"RFC3339"=设定
			Disabled              *bool           `json:"disabled"`                 // 缺省=不动；true=停用，false=恢复
			MaxRequestCostCredits *int64          `json:"max_request_cost_credits"` // 缺省=不动；0=不限
			AllowedModels         json.RawMessage `json:"allowed_models"`           // 缺省=不动；null=清除即不限；数组=整体设定
		}
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
		switch {
		case len(req.AllowedModels) == 0: // 字段缺省：不动
		case string(req.AllowedModels) == "null":
			patch.ClearAllowModels = true
		default:
			var models []string
			if err := json.Unmarshal(req.AllowedModels, &models); err != nil {
				writeAdminError(w, r, 400, protocol.TypeBadRequest, "bad_request", "allowed_models 须为字符串数组或 null")
				return
			}
			patch.AllowedModels = models // 空白条目整单拒绝在 auth 归一化，返回 400
		}
		switch {
		case len(req.ExpiresAt) == 0: // 字段缺省：不动
		case string(req.ExpiresAt) == "null":
			patch.ClearExpires = true
		default:
			var s string
			if err := json.Unmarshal(req.ExpiresAt, &s); err != nil {
				writeAdminError(w, r, 400, protocol.TypeBadRequest, "bad_request", "expires_at 须为 RFC3339 字符串或 null")
				return
			}
			t, perr := time.Parse(time.RFC3339, s)
			if perr != nil {
				writeAdminError(w, r, 400, protocol.TypeBadRequest, "bad_request", "expires_at 须为 RFC3339 形态")
				return
			}
			patch.ExpiresAt = &t
		}
		rec, err := auth.UpdateKeyLimits(d.DB, r.PathValue("id"), patch)
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
		n, err := auth.RevokeKeyByPrefix(d.DB, prefix)
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
		now := time.Now().UTC()
		parseTS := func(v, label string) (string, bool) {
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
		from, ok := parseTS(q.Get("from"), "from")
		if !ok {
			return
		}
		if from == "" {
			from = now.Add(-7 * 24 * time.Hour).Format(time.RFC3339)
		}
		to, ok := parseTS(q.Get("to"), "to")
		if !ok {
			return
		}
		if to == "" {
			to = now.Format(time.RFC3339)
		}
		if from > to {
			writeAdminError(w, r, 400, protocol.TypeBadRequest, "bad_request", "from 不可晚于 to")
			return
		}
		pts, err := store.UsageAggregate(d.DB, store.UsageDim(groupBy), from, to,
			q.Get("tenant_id"), q.Get("key_id"))
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
				st := "unknown"
				if pool := pools[p.Name]; pool != nil {
					st = pool.StateOf(kr.Env, now)
				}
				keys = append(keys, map[string]any{"id": kr.Env, "weight": kr.Weight, "state": st})
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
