package admin

import (
	"net/http"
	"strings"

	"github.com/solvela/muniu/internal/protocol"
)

// ---- balances 余额观测 ----
// 三个端点均为轻查询/信号投递:总览走服务层单源(与评估循环同一 observe 口径),
// manual 录入校验在服务层(kind/币种/金额),refresh 为异步信号,结果经总览可见。

func balancesOverview(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.Balance == nil {
			// 观测未启用是合法形态:返回空集而非错误,控制台呈现「未启用」空态
			writeJSON(w, 200, map[string]any{"enabled": false, "providers": []any{}})
			return
		}
		writeJSON(w, 200, map[string]any{
			"enabled": true, "providers": d.Balance.Overview(), "settlement": d.Balance.Settlement(),
		})
	}
}

func balanceManual(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.Balance == nil {
			writeAdminError(w, r, 400, protocol.TypeBadRequest, "balance_disabled", "余额观测未启用,请先在配置中声明渠道余额源")
			return
		}
		var req struct {
			Amount   string `json:"amount"`
			Currency string `json:"currency"`
		}
		if err := decode(r, &req); err != nil ||
			strings.TrimSpace(req.Amount) == "" || strings.TrimSpace(req.Currency) == "" {
			writeAdminError(w, r, 400, protocol.TypeBadRequest, "bad_request", "amount 与 currency 必填")
			return
		}
		provider := r.PathValue("provider")
		if !d.Balance.HasProvider(provider) {
			writeAdminError(w, r, 404, protocol.TypeNotFound, "provider_not_found", "渠道未声明余额源")
			return
		}
		if err := d.Balance.RecordManual(provider, strings.TrimSpace(req.Amount), strings.TrimSpace(req.Currency)); err != nil {
			// 金额/币种/来源不匹配均为用户可修正的录入错误,透出原始 message
			writeAdminError(w, r, 400, protocol.TypeBadRequest, "balance_manual_failed", err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "provider": provider})
	}
}

func balanceRefresh(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.Balance == nil {
			writeAdminError(w, r, 400, protocol.TypeBadRequest, "balance_disabled", "余额观测未启用,请先在配置中声明渠道余额源")
			return
		}
		provider := r.PathValue("provider")
		if !d.Balance.HasProvider(provider) {
			writeAdminError(w, r, 404, protocol.TypeNotFound, "provider_not_found", "渠道未声明余额源")
			return
		}
		d.Balance.RequestRefresh(provider) // 异步执行,结果经 GET /admin/v1/balances 可见
		writeJSON(w, 200, map[string]any{"ok": true, "provider": provider, "async": true})
	}
}
