// 预设目录端点契约:鉴权同管理面其余端点;响应 = 目录整表(presets 包装键)。
package admin_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/solvela/muniu/internal/presets"
)

func TestProviderPresets_契约(t *testing.T) {
	h := newAdmin(t, openDB(t), "tok", nil)

	// 未鉴权 → 401(与管理面统一门禁一致)
	if code, _ := call(t, h, http.MethodGet, "/admin/v1/provider-presets", "", ""); code != http.StatusUnauthorized {
		t.Fatalf("未鉴权应 401,实际 %d", code)
	}

	code, body := call(t, h, http.MethodGet, "/admin/v1/provider-presets", "tok", "")
	if code != http.StatusOK {
		t.Fatalf("应 200,实际 %d", code)
	}
	var resp struct {
		Presets []presets.Preset `json:"presets"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("响应非合法 JSON: %v", err)
	}
	want := presets.Catalog()
	if len(resp.Presets) != len(want) {
		t.Fatalf("预设条数不符: got %d want %d", len(resp.Presets), len(want))
	}
	for i, p := range resp.Presets {
		if p.Alias != want[i].Alias || p.BaseURL != want[i].BaseURL || p.Adapter != want[i].Adapter {
			t.Fatalf("第 %d 条漂移: got %+v want %+v", i, p, want[i])
		}
	}
}
