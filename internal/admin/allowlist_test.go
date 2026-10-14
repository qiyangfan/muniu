// key 级模型白名单管理面契约测试:签发归一化回显/空白条目 400、PATCH 三态、
// 未知 key 404、列表回显(先红后绿;helpers 复用 admin_test.go 同包设施)。
package admin_test

import (
	"encoding/json"
	"testing"
)

// parseKeyResp 提取签发/PATCH 响应中与本测试相关的字段。
type keyResp struct {
	ID            string   `json:"id"`
	AllowedModels []string `json:"allowed_models"`
}

func TestAdmin_key白名单签发三态与回显(t *testing.T) {
	db := openDB(t)
	h := newAdmin(t, db, "tok", nil)

	// 签发:trim+去重归一化后回显
	code, body := call(t, h, "POST", "/admin/v1/keys", "tok",
		`{"tenant":"default","name":"wl","allowed_models":[" m-a ","m-b","m-a"]}`)
	if code != 200 {
		t.Fatalf("签发失败: %d %s", code, body)
	}
	var created keyResp
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatalf("响应非 JSON: %s", body)
	}
	if len(created.AllowedModels) != 2 || created.AllowedModels[0] != "m-a" || created.AllowedModels[1] != "m-b" {
		t.Fatalf("签发回显应归一化: %v", created.AllowedModels)
	}

	// 空白条目:400 整单拒绝
	code, _ = call(t, h, "POST", "/admin/v1/keys", "tok",
		`{"tenant":"default","name":"bad","allowed_models":["m-a","  "]}`)
	if code != 400 {
		t.Fatalf("空白条目应 400 整单拒绝: %d", code)
	}

	// PATCH 三态:数组=整体设定
	code, body = call(t, h, "PATCH", "/admin/v1/keys/"+created.ID, "tok", `{"allowed_models":["m-c"]}`)
	if code != 200 {
		t.Fatalf("PATCH 失败: %d %s", code, body)
	}
	var patched keyResp
	_ = json.Unmarshal([]byte(body), &patched)
	if len(patched.AllowedModels) != 1 || patched.AllowedModels[0] != "m-c" {
		t.Fatalf("PATCH 设定回显不符: %v", patched.AllowedModels)
	}

	// PATCH null = 清除恢复不限
	code, body = call(t, h, "PATCH", "/admin/v1/keys/"+created.ID, "tok", `{"allowed_models":null}`)
	if code != 200 {
		t.Fatalf("PATCH null 失败: %d %s", code, body)
	}
	_ = json.Unmarshal([]byte(body), &patched)
	if len(patched.AllowedModels) != 0 {
		t.Fatalf("null 应清除白名单: %v", patched.AllowedModels)
	}

	// PATCH 省略 = 不动(先设一个值再发无关 PATCH)
	_, _ = call(t, h, "PATCH", "/admin/v1/keys/"+created.ID, "tok", `{"allowed_models":["m-d"]}`)
	code, body = call(t, h, "PATCH", "/admin/v1/keys/"+created.ID, "tok", `{"rpm_limit":77}`)
	if code != 200 {
		t.Fatalf("无关 PATCH 失败: %d %s", code, body)
	}
	_ = json.Unmarshal([]byte(body), &patched)
	if len(patched.AllowedModels) != 1 || patched.AllowedModels[0] != "m-d" {
		t.Fatalf("省略字段不得改动白名单: %v", patched.AllowedModels)
	}

	// 空白条目 PATCH:400 整单拒绝,既有白名单不变
	code, _ = call(t, h, "PATCH", "/admin/v1/keys/"+created.ID, "tok", `{"allowed_models":[" "]}`)
	if code != 400 {
		t.Fatalf("非法条目 PATCH 应 400: %d", code)
	}
	code, body = call(t, h, "GET", "/admin/v1/keys", "tok", "")
	if code != 200 || !containsField(body, "allowed_models") {
		t.Fatalf("列表应回显 allowed_models: %d %s", code, body)
	}

	// 未知 key:404
	code, _ = call(t, h, "PATCH", "/admin/v1/keys/no-such", "tok", `{"allowed_models":["x"]}`)
	if code != 404 {
		t.Fatalf("未知 key 应 404: %d", code)
	}
}

// containsField 轻量 JSON 字段存在性判定(列表响应结构断言足够)。
func containsField(body, field string) bool {
	var rows struct {
		Keys []map[string]any `json:"keys"`
	}
	if err := json.Unmarshal([]byte(body), &rows); err != nil {
		return false
	}
	for _, k := range rows.Keys {
		if _, ok := k[field]; ok {
			return true
		}
	}
	return false
}

// 空数组 PATCH = 设定为空 = 恢复不限(与 null 清除等价;归一化空→NULL 单源)。
func TestAdmin_key白名单空数组等价清除(t *testing.T) {
	db := openDB(t)
	h := newAdmin(t, db, "tok", nil)

	code, body := call(t, h, "POST", "/admin/v1/keys", "tok",
		`{"tenant":"default","name":"wl","allowed_models":["m-a"]}`)
	if code != 200 {
		t.Fatalf("签发失败: %d %s", code, body)
	}
	var created keyResp
	_ = json.Unmarshal([]byte(body), &created)

	code, body = call(t, h, "PATCH", "/admin/v1/keys/"+created.ID, "tok", `{"allowed_models":[]}`)
	if code != 200 {
		t.Fatalf("空数组 PATCH 失败: %d %s", code, body)
	}
	var patched keyResp
	_ = json.Unmarshal([]byte(body), &patched)
	if len(patched.AllowedModels) != 0 {
		t.Fatalf("空数组应恢复不限: %v", patched.AllowedModels)
	}
}
