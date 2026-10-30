package admin

import (
	"net/http"
)

// ---- upstream-health 上游健康观测 ----
// 只读端点:各渠道健康相位/降级原因/探针结果与各别名换道率。未启用(总开关关闭或未装配)
// 返回 enabled=false 空态,控制台不渲染健康元素。

func upstreamHealth(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.Health == nil {
			writeJSON(w, 200, map[string]any{"enabled": false, "providers": []any{}, "aliases": []any{}})
			return
		}
		providers, aliases := d.Health.Snapshot()
		writeJSON(w, 200, map[string]any{"enabled": true, "providers": providers, "aliases": aliases})
	}
}
