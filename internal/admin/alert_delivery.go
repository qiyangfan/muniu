package admin

import (
	"net/http"
	"time"

	"github.com/solvela/muniu/internal/store"
)

// ---- alerts 投递自监控 ----
// 只读端点:webhook 投递最终失败的单行事实(失败中/已解除/从未失败)。
// 递归纪律:本端点是失败状态的唯一可见面,失败状态绝不经 webhook 外发。

func alertDeliveryStatus(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !d.AlertWebhookConfigured {
			// 子系统关闭是合法形态:200 + configured=false,控制台不渲染任何投递状态元素
			writeJSON(w, 200, map[string]any{"configured": false, "failing": false})
			return
		}
		st, err := store.GetAlertDeliveryStatus(d.DB)
		if err != nil {
			respondErr(d, w, r, err, "alert_delivery_status_failed")
			return
		}
		out := map[string]any{"configured": true, "failing": false}
		if st != nil {
			out["failing"] = st.Failing
			out["last_event_key"] = st.LastEventKey
			out["last_severity"] = st.LastSeverity
			out["last_summary"] = st.LastSummary
			out["last_error"] = st.LastError
			out["failure_count"] = st.FailureCount
			out["last_failure_at"] = rfc3339OrEmpty(st.LastFailureAt)
			out["last_resolved_at"] = rfc3339OrEmpty(st.LastResolvedAt)
		}
		writeJSON(w, 200, out)
	}
}

// rfc3339OrEmpty 零时刻序列化为空串(前端按「无事实」渲染,不展示 1970)。
func rfc3339OrEmpty(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
