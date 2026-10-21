// 验证共享后端模式仍按 quota、RPM、TPM 的顺序执行限流检查。
package ratelimit

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/solvela/muniu/internal/auth"
)

// fakeShared 记录各闸调用，供链序断言。
type fakeShared struct {
	rpmCalled, tpmCalled bool
	rpmAllow             bool
}

func (f *fakeShared) AllowRPM(keyID string, rpm int64, now time.Time) (bool, time.Duration) {
	f.rpmCalled = true
	return f.rpmAllow, 2 * time.Second
}

func (f *fakeShared) AllowTPM(keyID string, limit int64, now time.Time) bool {
	f.tpmCalled = true
	return true
}

func (f *fakeShared) RecordTPM(keyID string, tokens int64, now time.Time) {}

func (f *fakeShared) Degraded() bool { return false }
func (f *fakeShared) Close() error   { return nil }

// sharedGate 构造带 quota（超限态）与共享后端的 Gate。
func sharedGate(t *testing.T, shared *fakeShared) *Gate {
	t.Helper()
	q := seedQuota(t, 0) // 租户不限；key 级 100 credits，由下方 insertUsage 灌满
	insertUsage(t, q, "r-1", quotaTenant, "key-shared", 100, time.Now())
	return &Gate{Quota: q, Shared: shared, Logger: slog.Default()}
}

func TestGateShared_quota闸在共享模式生效(t *testing.T) {
	shared := &fakeShared{rpmAllow: true}
	g := sharedGate(t, shared)
	info := &auth.KeyInfo{ID: "key-shared", TenantID: quotaTenant, RPMLimit: 60, TPMLimit: 0, DailyQuotaCredits: 100}
	proxy := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	rec := httptest.NewRecorder()
	g.Middleware(proxy).ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", nil).
		WithContext(auth.WithKeyInfo(context.Background(), info)))

	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("日配额超限应 402,得 %d(共享模式 quota 闸不得旁路)", rec.Code)
	}
	if shared.rpmCalled {
		t.Fatal("quota 已拒,RPM 共享闸不应再被调用(链序:quota → RPM → TPM)")
	}
}

func TestGateShared_未超限走共享RPM判定(t *testing.T) {
	shared := &fakeShared{rpmAllow: false}
	g := &Gate{Shared: shared, Logger: slog.Default()} // Quota nil：验证 nil 容忍与 RPM 判定
	info := &auth.KeyInfo{ID: "k9", TenantID: quotaTenant, RPMLimit: 60}
	proxy := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	rec := httptest.NewRecorder()
	g.Middleware(proxy).ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", nil).
		WithContext(auth.WithKeyInfo(context.Background(), info)))

	if !shared.rpmCalled {
		t.Fatal("应调用共享 RPM 判定")
	}
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("RPM 拒绝应 429,得 %d", rec.Code)
	}
}
