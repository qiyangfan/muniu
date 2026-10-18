// 模型发现 e2e 钉子(gateway-parity 2.2):配额耗尽的 key 仍可列模型(诊断链路不被
// 成本闸阻断),而同一 key 的转发请求被 402 拒——证明模型链路确实在配额闸之外。
package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/solvela/muniu/internal/auth"
	"github.com/solvela/muniu/internal/store"
	"github.com/solvela/muniu/internal/testkit"
)

func TestE2E_配额耗尽仍可列模型(t *testing.T) {
	ts, _, adb := newE2E(t, nil, 200, testkit.JSONOKBody)

	// 签发日配额 50μ 的 key,并预插当日已耗成本 100μ(estimated=0,配额聚合口径)
	kdb := adb
	plaintext, rec, err := auth.CreateKey(kdb, "default", "quota-pin", 60, auth.KeyOpts{DailyQuotaCredits: 50})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.InsertUsageBatch(kdb, []store.UsageRow{{
		ID: uuid.NewV7().String(), RequestID: "req-quota-pin",
		TenantID: "01900000-0000-7000-8000-000000000001", APIKeyID: rec.ID,
		Provider: "openai", ModelAlias: e2eAlias, UpstreamModel: "upstream-a",
		Status: "ok", CostCredits: 100, LatencyTotalMs: 1,
		CreatedAt: time.Now().UTC(),
	}}); err != nil {
		t.Fatal(err)
	}

	do := func(method, path, key string) (int, string) {
		req, _ := http.NewRequest(method, ts.URL+path, strings.NewReader(e2eJSONBody))
		req.Header.Set("Authorization", "Bearer "+key)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return resp.StatusCode, string(body)
	}

	// 同一 key:转发被 402(配额闸真实生效)
	if code, body := do(http.MethodPost, "/v1/chat/completions", plaintext); code != http.StatusPaymentRequired {
		t.Fatalf("配额耗尽的转发应 402: %d %s", code, body)
	}
	// 列模型 200(诊断链路不被阻断)
	code, body := do(http.MethodGet, "/v1/models", plaintext)
	if code != http.StatusOK {
		t.Fatalf("配额耗尽仍应可列模型: %d %s", code, body)
	}
	var list struct {
		Data []struct{ ID string } `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Data) != 1 || list.Data[0].ID != e2eAlias {
		t.Fatalf("目录应含 %s: %v", e2eAlias, list.Data)
	}
	// 未鉴权 401(模型面鉴权同数据面)
	if code, _ := do(http.MethodGet, "/v1/models", "mk-invalid"); code != http.StatusUnauthorized {
		t.Fatalf("未鉴权应 401: %d", code)
	}
}
