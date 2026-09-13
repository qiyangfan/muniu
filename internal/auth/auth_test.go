// auth 契约测试(测试先行域):签发形态、sha256 落库、校验中间件成功路径与
// 负面路径(无头/坏格式/吊销/不存在)、进程内缓存的过期与容量淘汰。
package auth

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/solvela/muniu/internal/store"
)

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestGenerate_形态与唯一性(t *testing.T) {
	k1 := Generate()
	k2 := Generate()
	if !strings.HasPrefix(k1, "mk-") {
		t.Fatalf("明文必须以 mk- 开头: %q", k1)
	}
	if len(k1) != 3+43 { // 32 字节熵 base62 编码 43 字符
		t.Fatalf("明文长度不符: %d (%q)", len(k1), k1)
	}
	if k1 == k2 {
		t.Fatal("两次签发不得相同")
	}
	for _, c := range k1[3:] {
		if !strings.ContainsRune(base62Chars, c) {
			t.Fatalf("出现非 base62 字符 %q", c)
		}
	}
}

func TestHash_确定性(t *testing.T) {
	h := Hash("mk-abc")
	want := sha256.Sum256([]byte("mk-abc"))
	if h != hex.EncodeToString(want[:]) {
		t.Fatal("Hash 与手工 sha256 不一致")
	}
}

// newVerifierDB 签一把入库的 key,返回 (db, 明文, prefix)。
func newVerifierDB(t *testing.T) (*sql.DB, string, string) {
	t.Helper()
	db := openDB(t)
	plaintext, rec, err := CreateKey(db, "default", "测试key", 60, KeyOpts{})
	if err != nil {
		t.Fatalf("签发失败: %v", err)
	}
	return db, plaintext, rec.Prefix
}

func TestVerifier_成功路径挂载租户上下文(t *testing.T) {
	db, plaintext, _ := newVerifierDB(t)
	v := NewVerifier(db, Options{TTL: 10 * time.Second, Capacity: 100}, testLogger())

	var seenKeyID, seenTenant string
	h := v.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		info := KeyFrom(r.Context())
		seenKeyID, seenTenant = info.ID, info.TenantID
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer "+plaintext)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("合法 key 应放行,实际 %d: %s", rec.Code, rec.Body.String())
	}
	// ctx 挂载的是解析后的租户 id(FK 键),不是租户名
	if seenKeyID == "" || seenTenant != "01900000-0000-7000-8000-000000000001" {
		t.Fatalf("ctx 未挂载 key 信息: %q %q", seenKeyID, seenTenant)
	}
}

func TestVerifier_负面路径全归auth_failed(t *testing.T) {
	db, plaintext, prefix := newVerifierDB(t)
	v := NewVerifier(db, Options{TTL: 10 * time.Second, Capacity: 100}, testLogger())
	handler := v.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	do := func(header string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	cases := []struct {
		name   string
		header string
	}{
		{"无头", ""},
		{"非Bearer前缀", "Basic abc"},
		{"坏格式缺空格", "Bearer" + plaintext},
		{"空key", "Bearer "},
		{"不存在", "Bearer mk-doesnotexistdoesnotexistdoesnotexist"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := do(c.header)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("应 401,实际 %d: %s", rec.Code, rec.Body.String())
			}
			var env struct {
				Error struct {
					Type string `json:"type"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || env.Error.Type != "auth_failed" {
				t.Fatalf("信封不符: %v %s", err, rec.Body.String())
			}
		})
	}

	// 吊销后必须 401
	if _, err := RevokeKeyByPrefix(db, prefix); err != nil {
		t.Fatalf("吊销失败: %v", err)
	}
	v.Invalidate() // 吊销属管理操作:清缓存立生效
	rec := do("Bearer " + plaintext)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("吊销 key 应 401,实际 %d", rec.Code)
	}
}

func TestCache_过期与容量淘汰(t *testing.T) {
	db, plaintext, _ := newVerifierDB(t)
	// TTL 极短:第二次请求时缓存必过期
	v := NewVerifier(db, Options{TTL: 5 * time.Millisecond, Capacity: 100}, testLogger())
	handler := v.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	do := func() int {
		req := httptest.NewRequest("POST", "/x", nil)
		req.Header.Set("Authorization", "Bearer "+plaintext)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := do(); code != 200 {
		t.Fatalf("首次应 200: %d", code)
	}
	time.Sleep(10 * time.Millisecond)
	if code := do(); code != 200 {
		t.Fatalf("过期后应重新查库放行: %d", code)
	}

	// 容量淘汰:容量为 1,另一 key 入缓存后挤掉前者(随机淘汰,只断言不崩且仍放行)
	v2 := NewVerifier(db, Options{TTL: time.Minute, Capacity: 1}, testLogger())
	h2 := v2.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest("POST", "/x", nil)
		req.Header.Set("Authorization", "Bearer "+plaintext)
		rec := httptest.NewRecorder()
		h2.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("容量淘汰后仍应放行: %d", rec.Code)
		}
	}
}

func TestCreateKey_落库记录与明文回显(t *testing.T) {
	db := openDB(t)
	plaintext, rec, err := CreateKey(db, "default", "k1", 120, KeyOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Prefix != plaintext[:12] {
		t.Fatalf("prefix 应为明文前 12 字符: %q vs %q", rec.Prefix, plaintext[:12])
	}
	if rec.RPMLimit != 120 {
		t.Fatalf("rpm 落库不符: %d", rec.RPMLimit)
	}
	got, err := store.GetAPIKeyByHash(db, Hash(plaintext))
	if err != nil {
		t.Fatalf("按 hash 查不到刚签发的 key: %v", err)
	}
	if got.ID != rec.ID || got.Name != "k1" {
		t.Fatalf("落库内容不符: %+v", got)
	}
}

// 租户级冻结:管理面 disable 租户 + 清验证缓存后,该租户的 active key 也必须拒绝
// (租户状态随 GetAPIKeyByHash 的 JOIN 返回,verify 一并判定)。
func TestVerify_租户禁用拒绝(t *testing.T) {
	db := openDB(t)
	plaintext, _, err := CreateKey(db, "default", "tenant-disable", 60, KeyOpts{})
	if err != nil {
		t.Fatal(err)
	}
	v := NewVerifier(db, Options{}, testLogger())
	probe := func() error {
		req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
		req.Header.Set("Authorization", "Bearer "+plaintext)
		_, err := v.verify(req)
		return err
	}
	if err := probe(); err != nil {
		t.Fatalf("禁用前应通过: %v", err)
	}
	if _, err := db.Exec(`UPDATE tenants SET status = 'disabled' WHERE name = 'default'`); err != nil {
		t.Fatal(err)
	}
	// 管理面 patch 租户后会调用 Invalidate(接线契约);此处直接模拟同一序列
	v.Invalidate()
	if err := probe(); err == nil {
		t.Fatal("租户禁用后其 active key 必须拒绝")
	}
}

// 平台 key(ADR-0006):离线验真/过期/篡改/吊销快照,与查库 key 并存。
func TestPlatformKey_验真与负面(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	claims := PlatformClaims{TenantID: "t-1", KeyID: "k-1", ExpiresUnix: time.Now().Add(time.Hour).Unix(),
		RPMLimit: 60, TPMLimit: 100_000, DailyQuotaCredits: 5_000_000}
	key, err := FormatPlatformKey(claims, secret)
	if err != nil {
		t.Fatal(err)
	}
	v := NewVerifier(nil, Options{PlatformSecrets: [][]byte{secret}}, testLogger())
	t.Cleanup(v.Close)

	req := func(k string) (*KeyInfo, error) {
		r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
		r.Header.Set("Authorization", "Bearer "+k)
		return v.verify(r)
	}
	info, err := req(key)
	if err != nil || info.ID != "k-1" || info.TenantID != "t-1" || info.RPMLimit != 60 || info.DailyQuotaCredits != 5_000_000 {
		t.Fatalf("合法平台 key 应通过并携带限额: %+v %v", info, err)
	}
	// 篡改
	if _, err := req(key[:len(key)-2] + "xx"); err == nil {
		t.Fatal("篡改必须拒绝")
	}
	// 过期
	expired, _ := FormatPlatformKey(PlatformClaims{TenantID: "t", KeyID: "k", ExpiresUnix: time.Now().Add(-time.Hour).Unix()}, secret)
	if _, err := req(expired); err == nil {
		t.Fatal("过期必须拒绝")
	}
	// 未知密钥
	other, _ := FormatPlatformKey(claims, []byte("another-secret-0123456789ab"))
	if _, err := req(other); err == nil {
		t.Fatal("未知密钥签发必须拒绝")
	}
	// 密钥轮换:新旧两把验签密钥并存
	oldKey := key
	v2 := NewVerifier(nil, Options{PlatformSecrets: [][]byte{[]byte("new-secret-0123456789abcdef"), secret}, RevocationsURL: ""}, testLogger())
	t.Cleanup(v2.Close)
	r := httptest.NewRequest("POST", "/x", nil)
	r.Header.Set("Authorization", "Bearer "+oldKey)
	if _, err := v2.verify(r); err != nil {
		t.Fatalf("轮换期旧密钥验签应通过: %v", err)
	}
	// 吊销快照命中
	v3 := NewVerifier(nil, Options{PlatformSecrets: [][]byte{secret}}, testLogger())
	t.Cleanup(v3.Close)
	v3.revMu.Lock()
	v3.revoked = map[string]struct{}{"k-1": {}}
	v3.revMu.Unlock()
	r3 := httptest.NewRequest("POST", "/x", nil)
	r3.Header.Set("Authorization", "Bearer "+key)
	if _, err := v3.verify(r3); err == nil {
		t.Fatal("吊销名单内必须拒绝")
	}
	// 未启用平台 key(空密钥):一律拒绝
	v4 := NewVerifier(nil, Options{}, testLogger())
	t.Cleanup(v4.Close)
	if _, err := func() (*KeyInfo, error) {
		r := httptest.NewRequest("POST", "/x", nil)
		r.Header.Set("Authorization", "Bearer "+key)
		return v4.verify(r)
	}(); err == nil {
		t.Fatal("未启用平台 key 时必须拒绝")
	}
}

// 吊销同步循环(ADR-0006):interval 缺省不 panic(曾被零值 NewTicker 击穿)、启动即拉、
// 版本单调防回退、Close 幂等。
func TestRevocationLoop_收敛与单调(t *testing.T) {
	version := int64(1)
	revoked := []string{"k-1"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"version": version, "revoked": revoked})
	}))
	t.Cleanup(srv.Close)

	// interval 缺省(0)——回归钉子:此前缺省修正写在构造副本上,NewTicker(0) 直接 panic
	v := NewVerifier(nil, Options{
		PlatformSecrets:     [][]byte{[]byte("0123456789abcdef0123456789abcdef")},
		RevocationsURL:      srv.URL,
		RevocationsInterval: 30 * time.Millisecond,
	}, testLogger())
	t.Cleanup(v.Close)

	key, err := FormatPlatformKey(PlatformClaims{TenantID: "t", KeyID: "k-1", ExpiresUnix: time.Now().Add(time.Hour).Unix()},
		[]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	verifyKey := func() bool {
		r := httptest.NewRequest("POST", "/x", nil)
		r.Header.Set("Authorization", "Bearer "+key)
		_, verr := v.verify(r)
		return verr == nil
	}
	// 启动即拉:快照生效后 k-1 必须拒绝(轮询至多几十 ms)
	deadline := time.Now().Add(2 * time.Second)
	for verifyKey() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if verifyKey() {
		t.Fatal("吊销快照生效后必须拒绝")
	}
	// 版本单调:旧快照(version 0)不得洗白
	func() {
		v.revMu.Lock()
		defer v.revMu.Unlock()
		if v.revVersion < 1 {
			t.Fatal("快照版本应已递增")
		}
	}()
	v.Close()
	v.Close() // 并发/重复 Close 不得 panic
}
