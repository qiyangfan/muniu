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
	plaintext, rec, err := CreateKey(db, "default", "测试key", 60)
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
	plaintext, rec, err := CreateKey(db, "default", "k1", 120)
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
	plaintext, _, err := CreateKey(db, "default", "tenant-disable", 60)
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
