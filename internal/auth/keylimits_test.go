// key 生命周期契约测试(gateway-parity「api-key-lifecycle」):
// 签发粒度(全字段/缺省零回归/负值拒绝)、PATCH 局部更新(null 清过期/未知 key)、
// 过期判定(查库与缓存命中两路径、时钟注入、message 可区分)。
package auth

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/solvela/muniu/internal/store"
)

func TestCreateKey_全字段签发落库与回执(t *testing.T) {
	db := openDB(t)
	exp := time.Now().Add(24 * time.Hour).UTC().Truncate(time.Second)
	plaintext, rec, err := CreateKey(db, "default", "全字段", 120, KeyOpts{
		TPMLimit: 1000, DailyQuotaCredits: 5_000_000, ExpiresAt: &exp,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rec.TPMLimit != 1000 || rec.DailyQuotaCredits != 5_000_000 || rec.ExpiresAt == nil {
		t.Fatalf("回执字段不符: %+v", rec)
	}
	// 列表回显(管理面可见性契约)
	keys, err := ListKeys(db)
	if err != nil || len(keys) != 1 {
		t.Fatalf("列表失败: %v %v", keys, err)
	}
	if keys[0].TPMLimit != 1000 || keys[0].DailyQuotaCredits != 5_000_000 || keys[0].ExpiresAt == nil {
		t.Fatalf("列表字段不符: %+v", keys[0])
	}
	// 查库路径带出过期(鉴权消费面)
	k, err := store.GetAPIKeyByHash(db, Hash(plaintext))
	if err != nil {
		t.Fatal(err)
	}
	if k.TPMLimit != 1000 || k.DailyQuotaCredits != 5_000_000 || k.ExpiresAt == nil {
		t.Fatalf("查库字段不符: %+v", k)
	}
}

func TestCreateKey_缺省签发零回归(t *testing.T) {
	db := openDB(t)
	_, rec, err := CreateKey(db, "default", "缺省", 60, KeyOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if rec.TPMLimit != 0 || rec.DailyQuotaCredits != 0 || rec.ExpiresAt != nil {
		t.Fatalf("缺省签发应为 不限/永不过期: %+v", rec)
	}
}

func TestCreateKey_负值限额拒绝(t *testing.T) {
	db := openDB(t)
	if _, _, err := CreateKey(db, "default", "x", -1, KeyOpts{}); !store.IsBusiness(err) {
		t.Fatalf("负 rpm 应业务错: %v", err)
	}
	if _, _, err := CreateKey(db, "default", "x", 60, KeyOpts{TPMLimit: -5}); !store.IsBusiness(err) {
		t.Fatalf("负 tpm 应业务错: %v", err)
	}
}

func TestUpdateKeyLimits_局部更新与null清除(t *testing.T) {
	db := openDB(t)
	exp := time.Now().Add(time.Hour).UTC()
	_, rec, err := CreateKey(db, "default", "patch-me", 10, KeyOpts{TPMLimit: 100, DailyQuotaCredits: 7, ExpiresAt: &exp})
	if err != nil {
		t.Fatal(err)
	}
	newTPM := int64(200)
	got, err := UpdateKeyLimits(db, rec.ID, LimitsPatch{TPMLimit: &newTPM})
	if err != nil {
		t.Fatal(err)
	}
	if got.TPMLimit != 200 || got.RPMLimit != 10 || got.DailyQuotaCredits != 7 || got.ExpiresAt == nil {
		t.Fatalf("局部更新应只动 tpm: %+v", got)
	}
	// 显式 null 清除过期
	got, err = UpdateKeyLimits(db, rec.ID, LimitsPatch{ClearExpires: true})
	if err != nil {
		t.Fatal(err)
	}
	if got.ExpiresAt != nil {
		t.Fatalf("null 清除后应永不过期: %+v", got)
	}
	// 未知 key → ErrNotFound(管理面 404)
	if _, err := UpdateKeyLimits(db, "no-such-id", LimitsPatch{ClearExpires: true}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("未知 key 应 ErrNotFound: %v", err)
	}
	// 负值拒绝
	neg := int64(-1)
	if _, err := UpdateKeyLimits(db, rec.ID, LimitsPatch{RPMLimit: &neg}); !store.IsBusiness(err) {
		t.Fatalf("负 rpm 应业务错: %v", err)
	}
}

// 过期判定收口在 verify 单点:缓存命中路径同样以当前时钟判定(时钟注入证明)。
func TestVerify_过期判定含缓存命中路径(t *testing.T) {
	db := openDB(t)
	exp := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	plaintext, _, err := CreateKey(db, "default", "过期钉子", 60, KeyOpts{ExpiresAt: &exp})
	if err != nil {
		t.Fatal(err)
	}
	v := NewVerifier(db, Options{}, testLogger())
	cur := time.Now()
	v.now = func() time.Time { return cur }

	// 未过期:放行并写入缓存
	if _, err := v.verify(bearerReq(plaintext)); err != nil {
		t.Fatalf("未过期应放行: %v", err)
	}
	// 时钟推进越过过期时刻:缓存命中路径同样拒绝(过期生效不因缓存漂移)
	cur = exp.Add(time.Minute)
	_, err = v.verify(bearerReq(plaintext))
	if !errors.Is(err, errKeyExpired) {
		t.Fatalf("过期后应 errKeyExpired(含缓存命中): %v", err)
	}
	// 首次校验即已过期(查库路径)
	exp2 := time.Now().Add(-time.Minute).UTC()
	plaintext2, _, _ := CreateKey(db, "default", "已过期", 60, KeyOpts{ExpiresAt: &exp2})
	if _, err := v.verify(bearerReq(plaintext2)); !errors.Is(err, errKeyExpired) {
		t.Fatalf("签发即过期的 key 应拒绝: %v", err)
	}
}

// 过期与无效/吊销同归 401 auth_failed,message 可区分(用户动作不同:重签 vs 找管理员)。
func TestMiddleware_过期message可区分(t *testing.T) {
	db := openDB(t)
	exp := time.Now().Add(-time.Hour).UTC()
	plaintext, _, _ := CreateKey(db, "default", "msg", 60, KeyOpts{ExpiresAt: &exp})
	v := NewVerifier(db, Options{}, testLogger())
	rec := httptest.NewRecorder()
	v.Middleware(http.NotFoundHandler()).ServeHTTP(rec, bearerReq(plaintext))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("应 401: %d", rec.Code)
	}
	var body struct {
		Error struct {
			Type    string `json:"type"`
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Type != "auth_failed" || body.Error.Code != "auth_failed" {
		t.Fatalf("类型应仍为 auth_failed(错误类型表零膨胀): %+v", body.Error)
	}
	if !strings.Contains(body.Error.Message, "已过期") {
		t.Fatalf("message 应区分过期语义: %q", body.Error.Message)
	}
}

// bearerReq 构造带 Bearer 明文的请求(过期/鉴权测试共用)。
func bearerReq(plaintext string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	r.Header.Set("Authorization", "Bearer "+plaintext)
	return r
}

// ---- 启停(可逆禁用,key-disable-toggle)----

// 停用/恢复全链:PATCH 语义、查库与缓存命中两路径拒绝、恢复字段无损、列表回显。
func TestUpdateKeyLimits_停用与恢复(t *testing.T) {
	db := openDB(t)
	plaintext, rec, err := CreateKey(db, "default", "启停", 10, KeyOpts{TPMLimit: 100})
	if err != nil {
		t.Fatal(err)
	}
	v := NewVerifier(db, Options{}, testLogger())
	// 放行一次写入鉴权缓存(停用生效必须经 Invalidate 即时达成,不等 TTL)
	if _, err := v.verify(bearerReq(plaintext)); err != nil {
		t.Fatalf("停用前应放行: %v", err)
	}
	yes, no := true, false
	got, err := UpdateKeyLimits(db, rec.ID, LimitsPatch{Disabled: &yes})
	if err != nil || !got.Disabled {
		t.Fatalf("停用后回执应 Disabled=true: %+v err=%v", got, err)
	}
	v.Invalidate() // 管理路径失效协议(与 admin patchKey 同款)
	if _, err := v.verify(bearerReq(plaintext)); !errors.Is(err, errKeyDisabled) {
		t.Fatalf("停用后应 errKeyDisabled(含缓存命中后路径): %v", err)
	}
	// 恢复:其余字段无损
	got, err = UpdateKeyLimits(db, rec.ID, LimitsPatch{Disabled: &no})
	if err != nil || got.Disabled {
		t.Fatalf("恢复后回执应 Disabled=false: %+v err=%v", got, err)
	}
	if got.TPMLimit != 100 || got.RPMLimit != 10 {
		t.Fatalf("恢复应不动限额字段: %+v", got)
	}
	v.Invalidate()
	if _, err := v.verify(bearerReq(plaintext)); err != nil {
		t.Fatalf("恢复后应放行: %v", err)
	}
	// 列表回显停用状态
	if _, err := UpdateKeyLimits(db, rec.ID, LimitsPatch{Disabled: &yes}); err != nil {
		t.Fatal(err)
	}
	keys, err := ListKeys(db)
	if err != nil || len(keys) != 1 {
		t.Fatalf("列表失败: %v %v", keys, err)
	}
	if !keys[0].Disabled || keys[0].Status != "active" {
		t.Fatalf("列表应回显停用且 status=active: %+v", keys[0])
	}
}

// 停用与无效/吊销/过期同归 401 auth_failed,message 可区分(用户动作:找管理员启用)。
func TestMiddleware_停用message可区分(t *testing.T) {
	db := openDB(t)
	plaintext, rec, _ := CreateKey(db, "default", "停用msg", 60, KeyOpts{})
	yes := true
	if _, err := UpdateKeyLimits(db, rec.ID, LimitsPatch{Disabled: &yes}); err != nil {
		t.Fatal(err)
	}
	v := NewVerifier(db, Options{}, testLogger())
	httpRec := httptest.NewRecorder()
	v.Middleware(http.NotFoundHandler()).ServeHTTP(httpRec, bearerReq(plaintext))
	if httpRec.Code != http.StatusUnauthorized {
		t.Fatalf("应 401: %d", httpRec.Code)
	}
	var body struct {
		Error struct {
			Type    string `json:"type"`
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(httpRec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Type != "auth_failed" || body.Error.Code != "auth_failed" {
		t.Fatalf("类型应仍为 auth_failed(错误类型表零膨胀): %+v", body.Error)
	}
	if !strings.Contains(body.Error.Message, "已停用") {
		t.Fatalf("message 应区分停用语义: %q", body.Error.Message)
	}
}

// 吊销是终态:恢复操作不得复活吊销 key。
func TestVerify_吊销key恢复操作不复活(t *testing.T) {
	db := openDB(t)
	plaintext, rec, _ := CreateKey(db, "default", "吊销不复活", 60, KeyOpts{})
	if n, err := RevokeKeyByPrefix(db, rec.Prefix); err != nil || n != 1 {
		t.Fatalf("吊销失败: %d %v", n, err)
	}
	no := false
	if _, err := UpdateKeyLimits(db, rec.ID, LimitsPatch{Disabled: &no}); err != nil {
		t.Fatalf("对吊销 key 恢复操作本身不应报错(无害落库): %v", err)
	}
	v := NewVerifier(db, Options{}, testLogger())
	if _, err := v.verify(bearerReq(plaintext)); err == nil || errors.Is(err, errKeyDisabled) {
		t.Fatalf("吊销 key 不得经恢复操作复活: %v", err)
	}
}

// 请求级成本预算:签发粒度、PATCH 修改、鉴权链路携带、负值拒绝(热路径全链)。
func TestKeyBudget_签发PATCH与鉴权携带(t *testing.T) {
	db := openDB(t)
	plaintext, rec, err := CreateKey(db, "default", "预算", 60, KeyOpts{MaxRequestCostCredits: 500})
	if err != nil {
		t.Fatal(err)
	}
	if rec.MaxRequestCostCredits != 500 {
		t.Fatalf("签发回执应带预算: %+v", rec)
	}
	v := NewVerifier(db, Options{}, testLogger())
	info, err := v.verify(bearerReq(plaintext))
	if err != nil || info.MaxRequestCostCredits != 500 {
		t.Fatalf("鉴权应携带预算进热路径: %+v err=%v", info, err)
	}
	// PATCH 清零(=不限)
	zero := int64(0)
	got, err := UpdateKeyLimits(db, rec.ID, LimitsPatch{MaxRequestCostCredits: &zero})
	if err != nil || got.MaxRequestCostCredits != 0 {
		t.Fatalf("PATCH 清零应生效: %+v err=%v", got, err)
	}
	v.Invalidate()
	info, err = v.verify(bearerReq(plaintext))
	if err != nil || info.MaxRequestCostCredits != 0 {
		t.Fatalf("清零后鉴权应为不限: %+v err=%v", info, err)
	}
	// 负值拒绝(负数会被执行链解释为「不限」,与收紧意图相反)
	neg := int64(-1)
	if _, err := UpdateKeyLimits(db, rec.ID, LimitsPatch{MaxRequestCostCredits: &neg}); !store.IsBusiness(err) {
		t.Fatalf("负预算应业务错: %v", err)
	}
	if _, _, err := CreateKey(db, "default", "负预算", 60, KeyOpts{MaxRequestCostCredits: -2}); !store.IsBusiness(err) {
		t.Fatalf("负预算签发应业务错: %v", err)
	}
}

// 缺省零回归:不携带 Disabled 字段的 PATCH 行为与既有语义一致。
func TestUpdateKeyLimits_停用字段缺省零回归(t *testing.T) {
	db := openDB(t)
	plaintext, rec, err := CreateKey(db, "default", "缺省", 10, KeyOpts{})
	if err != nil {
		t.Fatal(err)
	}
	newRPM := int64(20)
	got, err := UpdateKeyLimits(db, rec.ID, LimitsPatch{RPMLimit: &newRPM})
	if err != nil {
		t.Fatal(err)
	}
	if got.Disabled {
		t.Fatalf("缺省 PATCH 不得改变停用态: %+v", got)
	}
	v := NewVerifier(db, Options{}, testLogger())
	if _, err := v.verify(bearerReq(plaintext)); err != nil {
		t.Fatalf("缺省 PATCH 后应照常放行: %v", err)
	}
}
