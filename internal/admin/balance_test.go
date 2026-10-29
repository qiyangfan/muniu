package admin_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/solvela/muniu/internal/admin"
	"github.com/solvela/muniu/internal/alerting"
	"github.com/solvela/muniu/internal/balance"
	"github.com/solvela/muniu/internal/store"
)

// newBalanceAdmin 装配带余额观测的管理面:manual 渠道 + 假上游 Bearer 渠道。
func newBalanceAdmin(t *testing.T) (http.Handler, *balance.Service) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "admin-balance.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"is_available":true,"balance_infos":[{"currency":"CNY","total_balance":"110.00"}]}`))
	}))
	t.Cleanup(stub.Close)
	q, err := balance.For(balance.SourceDeepseek, balance.SourceConfig{BaseURL: stub.URL})
	if err != nil {
		t.Fatal(err)
	}
	below := int64(5_000_000_000)
	bindings := map[string]balance.ChannelBinding{
		"ch":  {Kind: balance.SourceManual, Currency: "CNY"},
		"dsx": {Kind: balance.SourceDeepseek, Querier: q, BelowCredits: &below, Keys: []balance.ChannelKey{{ID: "DSK", Value: "sk"}}},
	}
	svc := balance.NewService(balance.Options{
		DB: db, Notifier: alerting.New(alerting.Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}),
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Sources: func() map[string]balance.ChannelBinding { return bindings },
	})
	svc.Start()
	t.Cleanup(svc.Close)
	h := admin.New(admin.Deps{
		DB: db, Token: "tk", Balance: svc,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if h == nil {
		t.Fatal("handler 不应为 nil")
	}
	return h, svc
}

func TestBalance_总览未启用返回空态(t *testing.T) {
	db := openDB(t)
	h := newAdmin(t, db, "tk", nil)
	status, body := call(t, h, "GET", "/admin/v1/balances", "tk", "")
	if status != 200 || !strings.Contains(body, `"enabled":false`) {
		t.Fatalf("未启用应返回 enabled=false: %d %s", status, body)
	}
}

func TestBalance_总览与manual录入链路(t *testing.T) {
	h, svc := newBalanceAdmin(t)
	// 录入即生效:400 路径 + 200 路径
	if status, body := call(t, h, "POST", "/admin/v1/balances/ch/manual", "tk", `{"amount":"abc","currency":"CNY"}`); status != 400 {
		t.Fatalf("非法金额应 400: %d %s", status, body)
	}
	if status, body := call(t, h, "POST", "/admin/v1/balances/ch/manual", "tk", `{"amount":"30","currency":"USD"}`); status != 400 {
		t.Fatalf("币种不一致应 400: %d %s", status, body)
	}
	if status, body := call(t, h, "POST", "/admin/v1/balances/dsx/manual", "tk", `{"amount":"10","currency":"CNY"}`); status != 400 {
		t.Fatalf("非 manual 渠道应 400: %d %s", status, body)
	}
	if status, _ := call(t, h, "POST", "/admin/v1/balances/ch/manual", "tk", `{"amount":"30","currency":"CNY"}`); status != 200 {
		t.Fatalf("合法录入应 200")
	}
	// 异步评估落位后总览可见
	deadline := time.Now().Add(3 * time.Second)
	var body string
	for time.Now().Before(deadline) {
		_, body = call(t, h, "GET", "/admin/v1/balances", "tk", "")
		if strings.Contains(body, `"min_effective":"30"`) {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if !strings.Contains(body, `"min_effective":"30"`) {
		t.Fatalf("总览应含录入渠道有效余额: %s", body)
	}
	// Bearer 渠道 refresh:异步查询后总览出现快照
	if status, _ := call(t, h, "POST", "/admin/v1/balances/dsx/refresh", "tk", ""); status != 200 {
		t.Fatalf("refresh 应受理")
	}
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_, body = call(t, h, "GET", "/admin/v1/balances", "tk", "")
		if strings.Contains(body, `"key_id":"DSK"`) && strings.Contains(body, `"balance":"110"`) {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if !strings.Contains(body, `"balance":"110"`) {
		t.Fatalf("refresh 后总览应含 DSK 快照: %s", body)
	}
	// 未声明渠道 404
	if status, _ := call(t, h, "POST", "/admin/v1/balances/ghost/refresh", "tk", ""); status != 404 {
		t.Fatalf("未声明渠道 refresh 应 404")
	}
	_ = svc
}

func TestBalance_未启用与未声明的防御路径(t *testing.T) {
	// 未启用(Balance=nil):manual/refresh 均 400 balance_disabled
	h0 := newAdmin(t, openDB(t), "tk", nil)
	if status, body := call(t, h0, "POST", "/admin/v1/balances/x/manual", "tk", `{"amount":"1","currency":"CNY"}`); status != 400 || !strings.Contains(body, "balance_disabled") {
		t.Fatalf("未启用 manual 应 400 balance_disabled: %d %s", status, body)
	}
	if status, body := call(t, h0, "POST", "/admin/v1/balances/x/refresh", "tk", ""); status != 400 || !strings.Contains(body, "balance_disabled") {
		t.Fatalf("未启用 refresh 应 400 balance_disabled: %d %s", status, body)
	}
	// 已启用但渠道未声明余额源:manual 404(与 refresh 同语义)
	h, _ := newBalanceAdmin(t)
	if status, body := call(t, h, "POST", "/admin/v1/balances/ghost/manual", "tk", `{"amount":"1","currency":"CNY"}`); status != 404 || !strings.Contains(body, "provider_not_found") {
		t.Fatalf("未声明渠道 manual 应 404: %d %s", status, body)
	}
}
