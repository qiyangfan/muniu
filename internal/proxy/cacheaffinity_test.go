// 会话缓存亲和契约:身份解析三叉(自报头/哈希兜底+噪声稳定/无user不参与/归属隔离)、
// 表 TTL+LRU+容量、缓存读方言提取、稳定前置。
package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/solvela/muniu/internal/protocol"
)

func stickReq(header, firstUser string) *http.Request {
	body := `{"model":"m","messages":[{"role":"system","content":"sys"},` +
		`{"role":"user","content":"` + firstUser + `"}]}`
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	if header != "" {
		r.Header.Set("X-Session-Id", header)
	}
	return r
}

func stickChat(t *testing.T, body string) *protocol.ChatRequest {
	t.Helper()
	var cr protocol.ChatRequest
	if err := json.Unmarshal([]byte(body), &cr); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	return &cr
}

func TestSessionStickID_三叉(t *testing.T) {
	r1 := stickReq("s-1", "hello")
	if got := sessionStickID(r1, "ak-1", stickChat(t,
		`{"model":"m","messages":[{"role":"system","content":"sys noise v2"},{"role":"user","content":"hello"}]}`)); !strings.HasPrefix(got, "h:") {
		t.Fatalf("自报头优先: %q", got)
	}
	r2 := stickReq("", "hello")
	a := sessionStickID(r2, "ak-1", stickChat(t,
		`{"model":"m","messages":[{"role":"system","content":"sys v1"},{"role":"user","content":"hello"}]}`))
	b := sessionStickID(r2, "ak-1", stickChat(t,
		`{"model":"m","messages":[{"role":"system","content":"sys v2 TODO 注入"},{"role":"user","content":"hello"}]}`))
	if a == "" || a != b {
		t.Fatalf("哈希身份应对 system 噪声稳定: %q vs %q", a, b)
	}
	if d := sessionStickID(r2, "ak-2", stickChat(t,
		`{"model":"m","messages":[{"role":"user","content":"hello"}]}`)); d == a {
		t.Fatal("跨 key 身份必须不同(归属隔离)")
	}
	if e := sessionStickID(stickReq("", ""), "ak-1", stickChat(t,
		`{"model":"m","messages":[{"role":"system","content":"s"}]}`)); e != "" {
		t.Fatalf("无 user 消息应返回空: %q", e)
	}
}

func TestCacheAffinityTable_TTL归属与清除(t *testing.T) {
	tbl := newCacheAffinityTable()
	now := time.Now()
	tbl.note("s1", "pa", "k1", "ak-1")
	if prov, key, st := tbl.lookup("s1", "ak-1", now.Add(time.Second)); st != "hit" || prov != "pa" || key != "k1" {
		t.Fatalf("命中失真: %s %s %s", prov, key, st)
	}
	if _, _, st := tbl.lookup("s1", "ak-2", now.Add(time.Second)); st != "miss" {
		t.Fatalf("跨 key 应不可见(miss 不泄露存在性): %s", st)
	}
	if _, _, st := tbl.lookup("s1", "ak-1", now.Add(cacheAffinityTTL+time.Second)); st != "stale" {
		t.Fatalf("过期应 stale: %s", st)
	}
	if _, _, st := tbl.lookup("s1", "ak-1", now); st != "miss" {
		t.Fatal("过期条目应被清除")
	}
	tbl.note("s2", "pa", "k2", "ak-1")
	tbl.drop("s2")
	if _, _, st := tbl.lookup("s2", "ak-1", now); st != "miss" {
		t.Fatal("drop 后应 miss")
	}
}

func TestCacheAffinityTable_容量淘汰(t *testing.T) {
	tbl := newCacheAffinityTable()
	for i := 0; i < cacheAffinityCapacity+8; i++ {
		tbl.note("s-"+itoa(i), "pa", "k", "ak")
	}
	tbl.mu.Lock()
	n := tbl.lru.Len()
	tbl.mu.Unlock()
	if n > cacheAffinityCapacity {
		t.Fatalf("超容量未淘汰: %d", n)
	}
}

func TestExtractCacheRead_方言键(t *testing.T) {
	cases := []struct {
		body string
		want bool
	}{
		{`{"prompt_tokens":5,"cached_tokens":4}`, true},
		{`{"prompt_cache_hit_tokens":9}`, true},
		{`{"cache_read_input_tokens":7}`, true},
		{`{"usage":{"cached_tokens":3}}`, true},
		{`{"prompt_tokens":5}`, false},
		{`{"cached_tokens":0}`, false},
		{`not-json`, false},
	}
	for _, tc := range cases {
		if got := extractCacheRead(json.RawMessage(tc.body)); (got > 0) != tc.want {
			t.Fatalf("%s: got %d want %v", tc.body, got, tc.want)
		}
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var digits []byte
	for i > 0 {
		digits = append([]byte{byte('0' + i%10)}, digits...)
		i /= 10
	}
	return string(digits)
}
