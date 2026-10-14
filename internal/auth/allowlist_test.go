// key 级模型白名单契约测试(测试先行域,先红后绿):签发回显与读侧贯通、归一化
// (trim 非空/去重/整单拒绝)、三态 PATCH、verify 携带白名单与缓存可见性、
// 损坏 JSON 宁拒不放行(expires_at 损坏同款纪律)。
package auth

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/solvela/muniu/internal/store"
)

func createAllowKey(t *testing.T, db *sql.DB, allowed []string) (string, *KeyRecord) {
	t.Helper()
	plaintext, rec, err := CreateKey(db, "default", "wl-key", 60, KeyOpts{AllowedModels: allowed})
	if err != nil {
		t.Fatalf("签发失败: %v", err)
	}
	return plaintext, rec
}

func verifyWith(t *testing.T, v *Verifier, plaintext string) *KeyInfo {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer "+plaintext)
	info, err := v.verify(req)
	if err != nil {
		t.Fatalf("verify 失败: %v", err)
	}
	return info
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// 签发回显即持久:KeyRecord 回执与 GetAPIKeyByHash 读侧同值(执行链的事实源)。
func TestAllowlist_签发回显与读侧贯通(t *testing.T) {
	db := openDB(t)
	plaintext, rec := createAllowKey(t, db, []string{"m-flash", "m-pro"})
	if !sameStrings(rec.AllowedModels, []string{"m-flash", "m-pro"}) {
		t.Fatalf("回执回显不符: %v", rec.AllowedModels)
	}
	row, err := store.GetAPIKeyByHash(db, Hash(plaintext))
	if err != nil {
		t.Fatalf("读侧查询失败: %v", err)
	}
	if !sameStrings(row.AllowedModels, []string{"m-flash", "m-pro"}) {
		t.Fatalf("读侧白名单不符: %v", row.AllowedModels)
	}
}

// 归一化:空白条目整单拒绝;合法条目 trim + 去重(保持首现顺序)。
func TestAllowlist_归一化_空条目拒绝与去重(t *testing.T) {
	db := openDB(t)
	if _, _, err := CreateKey(db, "default", "bad", 60, KeyOpts{AllowedModels: []string{"m-a", "   "}}); err == nil {
		t.Fatal("含空白条目的签发必须整单拒绝")
	}
	_, rec, err := CreateKey(db, "default", "ok", 60, KeyOpts{AllowedModels: []string{" m-b ", "m-b", "m-a"}})
	if err != nil {
		t.Fatalf("合法签发失败: %v", err)
	}
	if !sameStrings(rec.AllowedModels, []string{"m-b", "m-a"}) {
		t.Fatalf("应 trim+去重且保序: %v", rec.AllowedModels)
	}
}

// PATCH 三态:数组=整体设定(null 语义归 ClearAllowModels);省略=不动;非法条目整单拒绝不落库。
func TestAllowlist_三态PATCH(t *testing.T) {
	db := openDB(t)
	_, rec := createAllowKey(t, db, []string{"m-a"})

	next := []string{"m-b"}
	rec2, err := UpdateKeyLimits(db, rec.ID, LimitsPatch{AllowedModels: next})
	if err != nil {
		t.Fatalf("设定失败: %v", err)
	}
	if !sameStrings(rec2.AllowedModels, []string{"m-b"}) {
		t.Fatalf("设定后回显不符: %v", rec2.AllowedModels)
	}

	rec3, err := UpdateKeyLimits(db, rec.ID, LimitsPatch{ClearAllowModels: true})
	if err != nil {
		t.Fatalf("清除失败: %v", err)
	}
	if len(rec3.AllowedModels) != 0 {
		t.Fatalf("清除后应恢复不限: %v", rec3.AllowedModels)
	}

	rpm := int64(120)
	rec4, err := UpdateKeyLimits(db, rec.ID, LimitsPatch{RPMLimit: &rpm})
	if err != nil {
		t.Fatalf("无关 PATCH 失败: %v", err)
	}
	if len(rec4.AllowedModels) != 0 {
		t.Fatalf("省略字段不得改动白名单: %v", rec4.AllowedModels)
	}

	if _, err := UpdateKeyLimits(db, rec.ID, LimitsPatch{AllowedModels: []string{" "}}); err == nil {
		t.Fatal("空白条目 PATCH 必须整单拒绝")
	}
	row, err := store.GetAPIKeyByHash(db, rowHash(t, db, rec.ID))
	if err != nil {
		t.Fatal(err)
	}
	if len(row.AllowedModels) != 0 {
		t.Fatalf("被拒 PATCH 不得改动白名单: %v", row.AllowedModels)
	}
}

// rowHash 按 key id 反查 hash(白名单读侧断言用;测试辅助,生产路径无此形态)。
func rowHash(t *testing.T, db *sql.DB, keyID string) string {
	t.Helper()
	var h string
	if err := db.QueryRow(`SELECT key_hash FROM api_keys WHERE id = ?`, keyID).Scan(&h); err != nil {
		t.Fatal(err)
	}
	return h
}

// verify 携带白名单;缓存 TTL 内旧值可见;Invalidate 后新值生效(与限额同款协议)。
func TestAllowlist_verify携带与缓存可见性(t *testing.T) {
	db := openDB(t)
	plaintext, rec := createAllowKey(t, db, []string{"m-a"})
	v := NewVerifier(db, Options{}, testLogger())
	defer v.Close()

	info := verifyWith(t, v, plaintext)
	if !sameStrings(info.AllowedModels, []string{"m-a"}) {
		t.Fatalf("KeyInfo 应携带白名单: %v", info.AllowedModels)
	}

	next := []string{"m-a", "m-b"}
	if _, err := UpdateKeyLimits(db, rec.ID, LimitsPatch{AllowedModels: next}); err != nil {
		t.Fatal(err)
	}
	// 缓存窗口内旧值可见(TTL 语义,只影响 DB 变更可见性)
	info = verifyWith(t, v, plaintext)
	if !sameStrings(info.AllowedModels, []string{"m-a"}) {
		t.Fatalf("缓存窗口内应仍见旧白名单: %v", info.AllowedModels)
	}
	v.Invalidate()
	info = verifyWith(t, v, plaintext)
	if !sameStrings(info.AllowedModels, []string{"m-a", "m-b"}) {
		t.Fatalf("失效后应见新白名单: %v", info.AllowedModels)
	}
}

// 列损坏(非法 JSON)按查询错误拒绝该 key——不放行语义不明的凭据(expires_at 损坏同款纪律)。
func TestAllowlist_损坏JSON拒不放行(t *testing.T) {
	db := openDB(t)
	plaintext, _ := createAllowKey(t, db, []string{"m-a"})
	if _, err := db.Exec(`UPDATE api_keys SET allowed_models = '{bad'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetAPIKeyByHash(db, Hash(plaintext)); err == nil {
		t.Fatal("allowed_models 列损坏必须拒绝该 key")
	}
}
