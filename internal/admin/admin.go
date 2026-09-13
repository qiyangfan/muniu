// Package admin 承载管理面(T4.1):/admin/v1/* 路由,鉴权 = MUNIU_ADMIN_TOKEN(Bearer,单管理员)。
// token 未配置 → Handler 返回 nil,管理面整体不注册(装配期 warn)。
// keys 管理与 Phase 2 CLI 同源(复用 auth 库函数);错误信封与业务面同格式。
package admin

import (
	"database/sql"
	"encoding/json"
	"errors"
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

// CacheStatsProvider 语义缓存统计源(装配注入;nil 时统计端点返回空态)。
type CacheStatsProvider func() map[string]any

// StaticEntry YAML 静态兜底价目的展示形态(GET 列表 yaml 来源行;窗口如实透出,
// 防止控制台编辑 yaml 行时静默丢窗)。
type StaticEntry struct {
	Def         metering.Pricing
	Timezone    string
	TimeWindows []PricingWindowView
}

// Deps 管理面依赖。Verifier 供吊销 key / 变更租户后即时清空鉴权缓存
// (不注入则依赖 TTL 自然收敛,「立即生效」承诺不成立)。
// Pricing 三件套支撑动态改价:PricingDir 转发热路径消费的目录;StaticPricing 为 YAML 静态兜底价
// (GET 列表展示用);RefreshPricing 由装配层注入——持久化成功后重建目录并原子替换。
type Deps struct {
	DB         *sql.DB
	Token      string
	Providers  []config.Provider
	Pools      map[string]*keypool.Pool
	CacheStats CacheStatsProvider
	Verifier   *auth.Verifier

	PricingDir     *metering.PricingDirectory
	StaticPricing  map[string]StaticEntry
	RefreshPricing func() error
	Plugins        *plugin.Registry // WASM 插件注册表(ADR-0011);nil = 端点返回禁用态

	Logger *slog.Logger
}

// New 构造管理面 handler;token 为空返回 nil(管理面关闭)。
func New(d Deps) http.Handler {
	if d.Token == "" {
		return nil
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /admin/v1/tenants", listTenants(d))
	mux.HandleFunc("POST /admin/v1/tenants", createTenant(d))
	mux.HandleFunc("PATCH /admin/v1/tenants/{id}", patchTenant(d))
	mux.HandleFunc("GET /admin/v1/keys", listKeys(d))
	mux.HandleFunc("POST /admin/v1/keys", createKey(d))
	mux.HandleFunc("PATCH /admin/v1/keys/{id}", patchKey(d))
	mux.HandleFunc("DELETE /admin/v1/keys/{prefix}", revokeKey(d))
	mux.HandleFunc("GET /admin/v1/usage", usage(d))
	mux.HandleFunc("GET /admin/v1/providers", providers(d))
	mux.HandleFunc("GET /admin/v1/cache/stats", cacheStats(d))
	mux.HandleFunc("GET /admin/v1/pricing", pricingList(d))
	mux.HandleFunc("PUT /admin/v1/pricing/{alias}", pricingPut(d))
	mux.HandleFunc("DELETE /admin/v1/pricing/{alias}", pricingDelete(d))
	registerPlugins(mux, d)
	return authAdmin(d.Token, d.Logger, mux)
}

// authAdmin 管理面鉴权:Bearer token 恒等比较(用 constant time 防时序侧信道)。
func authAdmin(token string, logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !secureEqual(got, token) {
			logger.Warn("管理面鉴权失败", "request_id", logging.RequestIDFrom(r.Context()))
			writeAdminError(w, r, http.StatusUnauthorized, protocol.TypeAuthFailed, "auth_failed", "管理 token 无效")
			return
		}
		next.ServeHTTP(w, r)
	})
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

// respondErr 错误出口分类:业务错(校验/冲突/不存在,store.MarkBusiness 标记)以 400+原样
// 可读 message 返回;其余(DB I/O 等内部故障)收敛为 500+「内部错误」,细节只进日志
// (conventions 错误纪律对管理面同样生效,驱动错误串不外泄)。
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
	return json.NewDecoder(r.Body).Decode(v)
}

// ---- tenants ----

func listTenants(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tenants, err := store.ListTenants(d.DB)
		if err != nil {
			// 内部错误细节只进日志,对外统一归一 message(conventions 错误纪律)
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
		// 负额度会被 quota/tpm 判定解释为「不限」,与收紧意图相反,入口即拒
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
			Tenant            string `json:"tenant"`
			Name              string `json:"name"`
			RPMLimit          int64  `json:"rpm_limit"`
			TPMLimit          int64  `json:"tpm_limit"`
			DailyQuotaCredits int64  `json:"daily_quota_credits"`
			ExpiresAt         string `json:"expires_at"` // 可空;RFC3339(空 = 永不过期)
		}
		if err := decode(r, &req); err != nil || req.Name == "" {
			writeAdminError(w, r, 400, protocol.TypeBadRequest, "bad_request", "name 必填")
			return
		}
		opts := auth.KeyOpts{TPMLimit: req.TPMLimit, DailyQuotaCredits: req.DailyQuotaCredits}
		if req.ExpiresAt != "" {
			t, err := time.Parse(time.RFC3339, req.ExpiresAt)
			if err != nil {
				writeAdminError(w, r, 400, protocol.TypeBadRequest, "bad_request", "expires_at 须为 RFC3339 形态")
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
			"key": plaintext, // 管理面明文仅此一次(与 CLI 同语义)
			"id":  rec.ID, "prefix": rec.Prefix, "tenant": rec.TenantID,
			"rpm_limit": rec.RPMLimit, "tpm_limit": rec.TPMLimit,
			"daily_quota_credits": rec.DailyQuotaCredits, "expires_at": rec.ExpiresAt,
		})
	}
}

// patchKey key 运行态限额局部更新(PATCH 语义:省略字段不动,expires_at 显式 null 清除)。
// 写后 Invalidate:限额是 DB 可变状态,必须走吊销同款失效协议,下一请求即新限额。
func patchKey(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			RPMLimit          *int64          `json:"rpm_limit"`
			TPMLimit          *int64          `json:"tpm_limit"`
			DailyQuotaCredits *int64          `json:"daily_quota_credits"`
			ExpiresAt         json.RawMessage `json:"expires_at"` // 缺省=不动;null=清除;"RFC3339"=设定
		}
		if err := decode(r, &req); err != nil {
			writeAdminError(w, r, 400, protocol.TypeBadRequest, "bad_request", "请求体非法")
			return
		}
		patch := auth.LimitsPatch{
			RPMLimit: req.RPMLimit, TPMLimit: req.TPMLimit, DailyQuotaCredits: req.DailyQuotaCredits,
		}
		switch {
		case len(req.ExpiresAt) == 0: // 字段缺省:不动
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
			d.Verifier.Invalidate() // 新限额下一请求即生效,不等缓存 TTL
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
			d.Verifier.Invalidate() // 吊销即时生效,不等缓存 TTL
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
			// 必须归一为 UTC:usage_logs.created_at 存 Z 后缀形态,SQL 按字典序比较,
			// 带 +08:00 之类偏移的入参不归一会让区间过滤整体错位
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
		out := make([]map[string]any, 0, len(d.Providers))
		for _, p := range d.Providers {
			keys := make([]map[string]any, 0, len(p.Keys))
			for _, kr := range p.Keys {
				st := "unknown"
				if pool := d.Pools[p.Name]; pool != nil {
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

// derefStr 指针转值(nil→空串,UpdateTenantPatch 以空串表达「不动」)。
func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// Routes 管理面注册的路由模式表(漂移门禁与装配层共享;顺序稳定)。
func Routes() []string {
	return []string{
		"GET /admin/v1/tenants",
		"POST /admin/v1/tenants",
		"PATCH /admin/v1/tenants/{id}",
		"GET /admin/v1/keys",
		"POST /admin/v1/keys",
		"PATCH /admin/v1/keys/{id}",
		"DELETE /admin/v1/keys/{prefix}",
		"GET /admin/v1/usage",
		"GET /admin/v1/providers",
		"GET /admin/v1/cache/stats",
		"GET /admin/v1/pricing",
		"PUT /admin/v1/pricing/{alias}",
		"DELETE /admin/v1/pricing/{alias}",
		"GET /admin/v1/plugins",
		"PUT /admin/v1/plugins/{name}",
		"PATCH /admin/v1/plugins/{name}",
	}
}
