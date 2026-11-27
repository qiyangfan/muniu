// 租户级配额闸测试：租户上限不被 key 上限掩盖、key 上限独立、快照缺失/租户未知 fail-closed。
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

func gateWithQuotas(quotas map[string]TenantQuota, ready bool) (*Gate, *MemoryTenantQuotas) {
	mem := NewMemoryTenantQuotas()
	if ready {
		mem.Replace(quotas)
	}
	g := &Gate{
		RPM: New(), TPM: NewTPM(), TenantRPM: New(), TenantTPM: NewTPM(),
		Quota:  NewQuota(nil, Options{Source: mem}, slog.Default()),
		Logger: slog.Default(),
	}
	return g, mem
}

func doGate(t *testing.T, g *Gate, info auth.KeyInfo) int {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/v1/chat/completions", nil)
	req = req.WithContext(auth.WithKeyInfo(req.Context(), &info))
	rec := httptest.NewRecorder()
	g.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, req)
	return rec.Code
}

func TestGate_租户RPM不被key上限掩盖(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	old := timeNow
	timeNow = func() time.Time { return now }
	t.Cleanup(func() { timeNow = old })
	g, _ := gateWithQuotas(map[string]TenantQuota{"t1": {RPM: 2}}, true)
	if code := doGate(t, g, auth.KeyInfo{ID: "k1", TenantID: "t1", RPMLimit: 100}); code != 200 {
		t.Fatalf("第 1 次应放行: %d", code)
	}
	if code := doGate(t, g, auth.KeyInfo{ID: "k1", TenantID: "t1", RPMLimit: 100}); code != 200 {
		t.Fatalf("第 2 次应放行: %d", code)
	}
	if code := doGate(t, g, auth.KeyInfo{ID: "k1", TenantID: "t1", RPMLimit: 100}); code != 429 {
		t.Fatalf("租户 RPM 耗尽应 429(key 仍充足): %d", code)
	}
}

func TestGate_key上限独立生效(t *testing.T) {
	g, _ := gateWithQuotas(map[string]TenantQuota{"t1": {RPM: 100}}, true)
	if code := doGate(t, g, auth.KeyInfo{ID: "k1", TenantID: "t1", RPMLimit: 1}); code != 200 {
		t.Fatalf("第 1 次应放行: %d", code)
	}
	if code := doGate(t, g, auth.KeyInfo{ID: "k1", TenantID: "t1", RPMLimit: 1}); code != 429 {
		t.Fatalf("key RPM 耗尽应 429: %d", code)
	}
}

func TestGate_租户TPM(t *testing.T) {
	now := time.Unix(1_700_000_100, 0)
	old := timeNow
	timeNow = func() time.Time { return now }
	t.Cleanup(func() { timeNow = old })
	g, _ := gateWithQuotas(map[string]TenantQuota{"t1": {TPM: 10}}, true)
	g.TenantTPM.RecordTenant("t1", 10, now)
	// 租户窗口已满，key 窗口为空：仍拒绝
	if code := doGate(t, g, auth.KeyInfo{ID: "k1", TenantID: "t1", TPMLimit: 1000}); code != 429 {
		t.Fatalf("租户 TPM 耗尽应 429: %d", code)
	}
}

func TestGate_禁用租户统一拒绝(t *testing.T) {
	g, _ := gateWithQuotas(map[string]TenantQuota{"t1": {Status: "disabled", RPM: 100}}, true)
	if code := doGate(t, g, auth.KeyInfo{ID: "k1", TenantID: "t1", RPMLimit: 100}); code != 403 {
		t.Fatalf("禁用租户应 403(平台 key 不查库也必须拒绝): %d", code)
	}
	g2, _ := gateWithQuotas(map[string]TenantQuota{"t1": {Status: "active", RPM: 100}}, true)
	if code := doGate(t, g2, auth.KeyInfo{ID: "k1", TenantID: "t1", RPMLimit: 100}); code != 200 {
		t.Fatalf("active 租户应放行: %d", code)
	}
}

func TestGate_快照缺失与租户未知failClosed(t *testing.T) {
	g, _ := gateWithQuotas(nil, false)
	if code := doGate(t, g, auth.KeyInfo{ID: "k1", TenantID: "t1"}); code != 503 {
		t.Fatalf("快照未就绪应 503: %d", code)
	}
	g2, _ := gateWithQuotas(map[string]TenantQuota{"t2": {}}, true)
	if code := doGate(t, g2, auth.KeyInfo{ID: "k1", TenantID: "t1"}); code != 403 {
		t.Fatalf("租户不在目录应 403: %d", code)
	}
}
