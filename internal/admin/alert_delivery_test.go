// 投递自监控端点三态契约:未配置关闭态、已配置无失败、失败中、已解除。
package admin_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/solvela/muniu/internal/admin"
	"github.com/solvela/muniu/internal/store"
)

func newDeliveryAdmin(t *testing.T, configured bool) http.Handler {
	t.Helper()
	h := admin.New(admin.Deps{
		DB:                     openDB(t),
		Token:                  "t",
		AlertWebhookConfigured: configured,
		Logger:                 slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if h == nil {
		t.Fatal("token 非空时 Handler 不应为 nil")
	}
	return h
}

func deliveryStatus(t *testing.T, h http.Handler) map[string]any {
	t.Helper()
	code, body := call(t, h, "GET", "/admin/v1/alerts/delivery-status", "t", "")
	if code != 200 {
		t.Fatalf("status=%d body=%s", code, body)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("响应非 JSON: %s", body)
	}
	return out
}

func TestAdminDeliveryStatus_四态(t *testing.T) {
	// 未配置:关闭态,无事实字段
	out := deliveryStatus(t, newDeliveryAdmin(t, false))
	if out["configured"] != false || out["failing"] != false {
		t.Fatalf("关闭态不符: %v", out)
	}
	if _, ok := out["last_failure_at"]; ok {
		t.Fatalf("关闭态不应带事实字段: %v", out)
	}

	// 已配置无失败行:failing=false,无事实字段
	h := newDeliveryAdmin(t, true)
	out = deliveryStatus(t, h)
	if out["configured"] != true || out["failing"] != false {
		t.Fatalf("已配置无失败态不符: %v", out)
	}
	if _, ok := out["last_failure_at"]; ok {
		t.Fatalf("无失败行不应带事实字段: %v", out)
	}

	// 失败落库:failing=true + 事实
	db := openDB(t)
	at := time.Date(2026, 10, 30, 8, 0, 0, 0, time.UTC)
	if err := store.RecordAlertDeliveryFailure(db, "balance_tier:p", "warning", "渠道 p 余额 1 天档", "http_status_503", at); err != nil {
		t.Fatal(err)
	}
	h2 := admin.New(admin.Deps{
		DB: db, Token: "t", AlertWebhookConfigured: true,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	out = deliveryStatus(t, h2)
	if out["failing"] != true || out["last_event_key"] != "balance_tier:p" || out["failure_count"] != float64(1) {
		t.Fatalf("失败态不符: %v", out)
	}

	// 成功解除:failing=false + 解除时刻可见(历史失败事实保留)
	if err := store.RecordAlertDeliverySuccess(db, at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	out = deliveryStatus(t, h2)
	if out["failing"] != false {
		t.Fatalf("解除后 failing 应为 false: %v", out)
	}
	if out["last_resolved_at"] == "" || out["last_event_key"] != "balance_tier:p" {
		t.Fatalf("解除态应保留失败事实与解除时刻: %v", out)
	}
}
