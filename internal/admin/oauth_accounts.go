// OAuth 订阅账号管理端点(oauth-subscription-lane 契约):列表(脱敏)/手动录入/删除
// (引用校验)/授权发起/回调换 token。凭据值任何响应不回原文(明文落库事实由列表 meta
// 披露);操作审计由 auditOperators 中间件统一覆盖(非 GET)。
package admin

import (
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"uuid"

	"github.com/solvela/muniu/internal/oauth"
	"github.com/solvela/muniu/internal/protocol"
	"github.com/solvela/muniu/internal/store"
)

// oauthAccountIDRe 账号 ID 形态:配置引用主键,限定可读字符防注入路径/日志混淆。
var oauthAccountIDRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

// maskToken 凭据脱敏:长值前 6 字符 + 长度提示;短值整体掩码(前缀回原文=泄漏,评审域二 P3)。
func maskToken(s string) string {
	if s == "" {
		return ""
	}
	n := utf8.RuneCountInString(s)
	if n <= 6 {
		return "**(" + itoaLen(n) + " 字符)"
	}
	return string([]rune(s)[:6]) + "…(" + itoaLen(n) + " 字符)"
}

func itoaLen(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// oauthAccountView 列表投影:状态展示值(行状态 + 过期推导),凭据恒脱敏。
type oauthAccountView struct {
	ID                string `json:"id"`
	ProviderType      string `json:"provider_type"`
	Status            string `json:"status"` // active | auth_failed | expired(行 active 但已过期的展示推导)
	StatusReason      string `json:"status_reason"`
	ExpiresAt         string `json:"expires_at"`
	AccessTokenMasked string `json:"access_token_masked"`
	RefreshPresent    bool   `json:"refresh_present"` // refresh token 是否已录入(明文落库事实的如实提示)
}

func oauthAccountRowToView(r store.OAuthAccountRow, now time.Time) oauthAccountView {
	status := r.Status
	if r.Status == store.OAuthStatusActive && !r.ExpiresAt.IsZero() && r.ExpiresAt.Before(now) {
		status = "expired"
	}
	return oauthAccountView{
		ID: r.ID, ProviderType: r.ProviderType, Status: status, StatusReason: r.StatusReason,
		ExpiresAt:         r.ExpiresAt.UTC().Format(time.RFC3339),
		AccessTokenMasked: maskToken(r.AccessToken),
		RefreshPresent:    r.RefreshToken != "",
	}
}

func oauthAccountsList(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := store.ListOAuthAccounts(d.DB)
		if err != nil {
			writeAdminError(w, r, 500, protocol.TypeInternal, "oauth_list_failed", "账号列表读取失败")
			return
		}
		now := time.Now()
		out := make([]oauthAccountView, 0, len(rows))
		for _, row := range rows {
			out = append(out, oauthAccountRowToView(row, now))
		}
		writeJSON(w, 200, map[string]any{
			"accounts": out,
			"meta": map[string]any{
				// 契约披露:凭据明文落库(SQLite),缓解 = DB 文件权限与部署边界;备份含敏感凭据
				"plaintext_disclosure": "refresh token 以明文存储于本地数据库(无法哈希,刷新需原文),请以文件权限与部署边界保护;state-backup 备份因此含敏感凭据",
				"flow_configured":      d.OAuthFlow != nil,
			},
		})
	}
}

func oauthAccountsCreate(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID           string `json:"id"`
			ProviderType string `json:"provider_type"`
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			ExpiresAt    string `json:"expires_at"` // RFC3339;空 = 视为已过期,刷新器下一轮自愈
		}
		if err := decode(r, &req); err != nil {
			writeAdminError(w, r, 400, protocol.TypeBadRequest, "bad_request", "请求体须为 JSON")
			return
		}
		id := strings.TrimSpace(req.ID)
		if id == "" {
			id = uuid.NewV7().String()
		}
		if !oauthAccountIDRe.MatchString(id) {
			writeAdminError(w, r, 400, protocol.TypeBadRequest, "bad_request",
				"账号 id 须为 [a-zA-Z0-9][a-zA-Z0-9_-]{0,63}")
			return
		}
		pt := strings.TrimSpace(req.ProviderType)
		if pt == "" {
			pt = "claude"
		}
		now := time.Now()
		exp := time.Time{}
		if req.ExpiresAt != "" {
			t, perr := time.Parse(time.RFC3339, req.ExpiresAt)
			if perr != nil {
				writeAdminError(w, r, 400, protocol.TypeBadRequest, "bad_request", "expires_at 须为 RFC3339")
				return
			}
			exp = t
		}
		row := store.OAuthAccountRow{
			ID: id, ProviderType: pt, Status: store.OAuthStatusActive,
			AccessToken: strings.TrimSpace(req.AccessToken), RefreshToken: strings.TrimSpace(req.RefreshToken),
			ExpiresAt: exp, CreatedAt: now, UpdatedAt: now,
		}
		if row.AccessToken == "" || row.RefreshToken == "" {
			writeAdminError(w, r, 400, protocol.TypeBadRequest, "bad_request", "access_token 与 refresh_token 必填")
			return
		}
		if err := store.CreateOAuthAccount(d.DB, row); err != nil {
			// 仅唯一键冲突报 409;磁盘满等 DB 故障如实 500,不得伪装成「已存在」误导排障
			if errors.Is(err, store.ErrOAuthAccountExists) {
				writeAdminError(w, r, 409, protocol.TypeBadRequest, "oauth_account_exists", "账号已存在: "+id)
				return
			}
			writeAdminError(w, r, 500, protocol.TypeInternal, "internal", "账号创建失败")
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "id": id})
	}
}

func oauthAccountDelete(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		// 引用校验:拓扑内任何渠道 keys 仍引用该账号时拒绝删除(引用方列表透出)
		var referrers []string
		if d.Topology != nil {
			provs, _ := d.Topology()
			seen := map[string]bool{}
			for _, p := range provs {
				referenced := false
				for _, kr := range p.Keys {
					if kr.Account == id {
						referenced = true
						break
					}
				}
				if referenced && !seen[p.Name] {
					seen[p.Name] = true
					referrers = append(referrers, p.Name)
				}
			}
		}
		if len(referrers) > 0 {
			writeAdminError(w, r, 409, protocol.TypeBadRequest, "oauth_account_referenced",
				"账号仍被渠道引用,先移除配置引用再删除: "+strings.Join(referrers, ", "))
			return
		}
		if err := store.DeleteOAuthAccount(d.DB, id); err != nil {
			if errors.Is(err, store.ErrOAuthAccountNotFound) {
				writeAdminError(w, r, 404, protocol.TypeNotFound, "oauth_account_not_found", "账号不存在: "+id)
				return
			}
			writeAdminError(w, r, 500, protocol.TypeInternal, "internal", "账号删除失败")
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "id": id})
	}
}

func oauthAuthorize(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.OAuthFlow == nil {
			writeAdminError(w, r, 400, protocol.TypeBadRequest, "oauth_disabled",
				"OAuth 授权流未配置,请在 muniu.yaml 声明 oauth 段(authorize_url/token_url/client_id/callback_url)")
			return
		}
		var req struct {
			AccountID string `json:"account_id"` // 空 = 授权新账号;非空 = 重授权既有账号
		}
		if err := decode(r, &req); err != nil {
			writeAdminError(w, r, 400, protocol.TypeBadRequest, "bad_request", "请求体须为 JSON")
			return
		}
		if aid := strings.TrimSpace(req.AccountID); aid != "" {
			if !oauthAccountIDRe.MatchString(aid) {
				writeAdminError(w, r, 400, protocol.TypeBadRequest, "bad_request", "account_id 形态非法")
				return
			}
		}
		u, err := d.OAuthFlow.Begin(strings.TrimSpace(req.AccountID))
		if err != nil {
			writeAdminError(w, r, 500, protocol.TypeInternal, "oauth_begin_failed", err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"authorize_url": u})
	}
}

func oauthCallback(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.OAuthFlow == nil {
			writeAdminError(w, r, 400, protocol.TypeBadRequest, "oauth_disabled", "OAuth 授权流未配置")
			return
		}
		code := r.URL.Query().Get("code")
		state := r.URL.Query().Get("state")
		if code == "" || state == "" {
			writeAdminError(w, r, 400, protocol.TypeBadRequest, "bad_request", "回调缺 code 或 state")
			return
		}
		pair, accountID, err := d.OAuthFlow.Exchange(r.Context(), state, code)
		if err != nil {
			if errors.Is(err, oauth.ErrStateInvalid) {
				writeAdminError(w, r, 400, protocol.TypeBadRequest, "oauth_state_invalid",
					"state 非法、过期或已使用(重放拒绝)")
				return
			}
			writeAdminError(w, r, 502, protocol.TypeUpstreamError, "oauth_exchange_failed", err.Error())
			return
		}
		if pair.RefreshToken == "" {
			// 与手动录入同一校验口径:空 RT 账号入库后首轮刷新即空凭据重探或误判终态(评审域二 P1 协同)
			writeAdminError(w, r, 502, protocol.TypeUpstreamError, "oauth_exchange_failed",
				"token 端点响应缺 refresh_token,凭据不落库,请重新授权")
			return
		}
		now := time.Now()
		exp := now.Add(pair.ExpiresIn)
		if accountID != "" {
			// 重授权既有账号:凭据回写 + 状态恢复 active(刷新器终态的唯一恢复路径);
			// 目标账号不存在 → 以声明 ID 落库创建(对配置预引用账号发起授权是合法路径)
			uerr := store.UpdateOAuthAccountTokens(d.DB, accountID, pair.AccessToken, pair.RefreshToken, exp, now)
			switch {
			case uerr == nil:
				writeJSON(w, 200, map[string]any{"ok": true, "id": accountID, "mode": "reauthorize"})
			case errors.Is(uerr, store.ErrOAuthAccountNotFound):
				if cerr := store.CreateOAuthAccount(d.DB, store.OAuthAccountRow{
					ID: accountID, ProviderType: "claude", Status: store.OAuthStatusActive,
					AccessToken: pair.AccessToken, RefreshToken: pair.RefreshToken,
					ExpiresAt: exp, CreatedAt: now, UpdatedAt: now,
				}); cerr != nil {
					writeAdminError(w, r, 500, protocol.TypeInternal, "internal", "账号创建失败: "+accountID)
					return
				}
				writeJSON(w, 200, map[string]any{"ok": true, "id": accountID, "mode": "created"})
			default:
				writeAdminError(w, r, 500, protocol.TypeInternal, "internal", "账号凭据回写失败: "+accountID)
			}
			return
		}
		id := uuid.NewV7().String()
		if cerr := store.CreateOAuthAccount(d.DB, store.OAuthAccountRow{
			ID: id, ProviderType: "claude", Status: store.OAuthStatusActive,
			AccessToken: pair.AccessToken, RefreshToken: pair.RefreshToken,
			ExpiresAt: exp, CreatedAt: now, UpdatedAt: now,
		}); cerr != nil {
			writeAdminError(w, r, 500, protocol.TypeInternal, "internal", "账号创建失败")
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "id": id, "mode": "created"})
	}
}
