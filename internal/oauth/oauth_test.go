// OAuth flow 与刷新器契约测试:PKCE 形态、state 单次性、token 端点交互、
// 刷新判定表(终态/重探/跳过)。白盒:pending 表与判定函数在包内。
package oauth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"
)

// deterministicRand 固定字节流随机源:Begin 产物可断言。
func deterministicRand(b byte) func(int) ([]byte, error) {
	return func(n int) ([]byte, error) {
		out := make([]byte, n)
		for i := range out {
			out[i] = b
		}
		return out, nil
	}
}

// pkceOracle PKCE 形态的独立预言:不经实现的 b64url helper,直接以 stdlib
// base64url(no-padding)+sha256 计算期望——实现编码/哈希突变时两侧不再同变。
func pkceOracle(randByte byte) (verifier, challenge string) {
	verifier = base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{randByte}, 32))
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

func testCfg(base string) Config {
	return Config{
		AuthorizeURL: base + "/authorize",
		TokenURL:     base + "/token",
		ClientID:     "cid",
		CallbackURL:  "http://127.0.0.1:8787/admin/v1/oauth/callback",
	}
}

func TestFlow_Begin_PKCE形态与state登记(t *testing.T) {
	f := NewFlow(testCfg("https://sso.example"), nil, deterministicRand(7))
	u, err := f.Begin("acc-1")
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	parsed := mustParseURL(t, u)
	if parsed.Query().Get("response_type") != "code" ||
		parsed.Query().Get("client_id") != "cid" ||
		parsed.Query().Get("code_challenge_method") != "S256" {
		t.Fatalf("consent URL 基础参数失真: %s", u)
	}
	verifier, challenge := pkceOracle(7)
	if parsed.Query().Get("code_challenge") != challenge {
		t.Fatalf("challenge ≠ S256(verifier): %s", parsed.Query().Get("code_challenge"))
	}
	state := parsed.Query().Get("state")
	f.mu.Lock()
	p, ok := f.pending[state]
	f.mu.Unlock()
	if !ok || p.verifier != verifier || p.accountID != "acc-1" {
		t.Fatalf("state 未登记或绑定失真: ok=%v %+v", ok, p)
	}
}

func TestFlow_Exchange_成功与state单次性(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "at-2", "refresh_token": "rt-2", "expires_in": 3600,
		})
	}))
	defer srv.Close()
	f := NewFlow(testCfg(srv.URL), srv.Client(), deterministicRand(3))
	uri, _ := f.Begin("acc-9")
	state := mustParseURL(t, uri).Query().Get("state")
	wantVerifier, _ := pkceOracle(3)

	pair, accID, err := f.Exchange(context.Background(), state, "the-code")
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if accID != "acc-9" || pair.AccessToken != "at-2" || pair.RefreshToken != "rt-2" ||
		pair.ExpiresIn != time.Hour {
		t.Fatalf("换发结果失真: acc=%s %+v", accID, pair)
	}
	if gotBody["grant_type"] != "authorization_code" || gotBody["code"] != "the-code" {
		t.Fatalf("请求体失真: %v", gotBody)
	}
	if gotBody["code_verifier"] != wantVerifier {
		t.Fatalf("verifier 未随请求上送: %v", gotBody["code_verifier"])
	}
	if gotBody["redirect_uri"] != "http://127.0.0.1:8787/admin/v1/oauth/callback" || gotBody["client_id"] != "cid" {
		t.Fatalf("redirect_uri/client_id 未随换发上送: %v", gotBody)
	}
	// 单次性:同 state 重放拒绝,不产生任何凭据
	if _, _, err := f.Exchange(context.Background(), state, "the-code"); !errors.Is(err, ErrStateInvalid) {
		t.Fatalf("state 重放应拒绝, got %v", err)
	}
	// 未知 state 同判
	if _, _, err := f.Exchange(context.Background(), "unknown", "c"); !errors.Is(err, ErrStateInvalid) {
		t.Fatalf("未知 state 应拒绝, got %v", err)
	}
}

func TestFlow_Exchange_过期pending拒绝(t *testing.T) {
	f := NewFlow(testCfg("https://sso.example"), nil, deterministicRand(1))
	uri, _ := f.Begin("")
	state := mustParseURL(t, uri).Query().Get("state")
	f.mu.Lock()
	p := f.pending[state]
	p.createdAt = time.Now().Add(-2 * pendingTTL)
	f.pending[state] = p
	f.mu.Unlock()
	if _, _, err := f.Exchange(context.Background(), state, "c"); !errors.Is(err, ErrStateInvalid) {
		t.Fatalf("过期 pending 应拒绝, got %v", err)
	}
}

func TestFlow_Begin_非法AuthorizeURL回删pending(t *testing.T) {
	// Begin 先登记后校验:校验失败必须回删本 state,残留项会在 TTL 外悬挂(白盒断言)
	cfg := testCfg("https://sso.example")
	cfg.AuthorizeURL = "://bad"
	f := NewFlow(cfg, nil, deterministicRand(1))
	if _, err := f.Begin("acc-1"); err == nil {
		t.Fatal("非法 authorize_url 应报错")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.pending) != 0 {
		t.Fatalf("校验失败应回删 pending, 残留 %d 项", len(f.pending))
	}
}

func TestFlow_Token端点错误结构化(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
	}))
	defer srv.Close()
	f := NewFlow(testCfg(srv.URL), srv.Client(), deterministicRand(1))
	_, err := f.Refresh(context.Background(), "rt-bad")
	te, ok := err.(*TokenError)
	if !ok || te.Status != 400 || te.GrantType != "invalid_grant" {
		t.Fatalf("错误应结构化为 TokenError{400,invalid_grant}, got %T %v", err, err)
	}
	if !isAuthReject(err) {
		t.Fatal("400 invalid_grant 应判终态")
	}
}

// --- 刷新器判定表 ---

type memStore struct {
	mu     sync.Mutex
	accs   map[string]Account
	calls  []string // token 端点收到的 refresh_token 序列(跳过断言依据)
	fields map[string][2]string
}

func newMemStore(accs ...Account) *memStore {
	m := &memStore{accs: map[string]Account{}, fields: map[string][2]string{}}
	for _, a := range accs {
		m.accs[a.ID] = a
	}
	return m
}

func (m *memStore) List() ([]Account, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Account, 0, len(m.accs))
	for _, a := range m.accs {
		out = append(out, a)
	}
	return out, nil
}

func (m *memStore) UpdateTokens(id, at, rt string, exp time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	a := m.accs[id]
	a.AccessToken, a.RefreshToken, a.ExpiresAt, a.Status = at, rt, exp, StatusActive
	m.accs[id] = a
	return nil
}

func (m *memStore) SetStatus(id, status, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	a := m.accs[id]
	a.Status = status
	m.fields[id] = [2]string{status, reason}
	m.accs[id] = a
	return nil
}

func tokenServer(t *testing.T, st *memStore, status int, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		if rt, ok := req["refresh_token"].(string); ok {
			st.mu.Lock()
			st.calls = append(st.calls, rt)
			st.mu.Unlock()
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

func newTestRefresher(t *testing.T, st AccountStore, srv *httptest.Server, sync func()) (*Refresher, *memStore) {
	t.Helper()
	f := NewFlow(testCfg(srv.URL), srv.Client(), deterministicRand(1))
	r := NewRefresher(testCfg(srv.URL), st, f, nil, RefreshTuning{Interval: time.Minute, Window: time.Hour}, sync)
	return r, nil
}

func TestRefresher_判定表(t *testing.T) {
	now := time.Now()
	type tc struct {
		name   string
		acc    Account
		status int
		body   string
		probe  func(t *testing.T, st *memStore, synced *bool)
	}
	cases := []tc{
		{
			name:   "临期active_刷新成功回写",
			acc:    Account{ID: "a1", Status: StatusActive, RefreshToken: "rt-a1", ExpiresAt: now.Add(5 * time.Minute)},
			status: 200,
			body:   `{"access_token":"at-new","refresh_token":"rt-new","expires_in":7200}`,
			probe: func(t *testing.T, st *memStore, _ *bool) {
				a := st.accs["a1"]
				if a.AccessToken != "at-new" || a.RefreshToken != "rt-new" || a.Status != StatusActive {
					t.Fatalf("刷新回写失真: %+v", a)
				}
				if !a.ExpiresAt.After(now.Add(time.Hour)) {
					t.Fatalf("过期时刻未按 expires_in 推进: %v", a.ExpiresAt)
				}
				// 刷新请求必须上送存量 RT(发空/错 RT 的突变在上游 200 下恒绿)
				if len(st.calls) != 1 || st.calls[0] != "rt-a1" {
					t.Fatalf("刷新应上送存量 RT: %v", st.calls)
				}
			},
		},
		{
			name:   "远期active_零触碰",
			acc:    Account{ID: "a2", Status: StatusActive, RefreshToken: "rt-a2", ExpiresAt: now.Add(48 * time.Hour)},
			status: 200,
			body:   `{"access_token":"x","refresh_token":"x","expires_in":3600}`,
			probe: func(t *testing.T, st *memStore, _ *bool) {
				a := st.accs["a2"]
				if a.AccessToken != "" || len(st.calls) != 0 {
					t.Fatalf("远期账号不应刷新: calls=%v acc=%+v", st.calls, a)
				}
			},
		},
		{
			name:   "刷新401_终态",
			acc:    Account{ID: "a3", Status: StatusActive, RefreshToken: "rt-a3", ExpiresAt: now.Add(time.Minute)},
			status: 401,
			body:   `{}`,
			probe: func(t *testing.T, st *memStore, _ *bool) {
				a := st.accs["a3"]
				if a.Status != StatusAuthFailed {
					t.Fatalf("401 应终态, got %q", a.Status)
				}
				if v := st.fields["a3"]; v[0] != StatusAuthFailed || v[1] == "" {
					t.Fatalf("终态原因未留痕: %v", v)
				}
			},
		},
		{
			name:   "刷新400invalid_grant_终态",
			acc:    Account{ID: "a4", Status: StatusActive, RefreshToken: "rt-a4", ExpiresAt: now.Add(time.Minute)},
			status: 400,
			body:   `{"error":"invalid_grant"}`,
			probe: func(t *testing.T, st *memStore, _ *bool) {
				if st.accs["a4"].Status != StatusAuthFailed {
					t.Fatalf("invalid_grant 应终态, got %q", st.accs["a4"].Status)
				}
			},
		},
		{
			name:   "刷新403_终态(spec MUST 双边钉住)",
			acc:    Account{ID: "a7", Status: StatusActive, RefreshToken: "rt-a7", ExpiresAt: now.Add(time.Minute)},
			status: 403,
			body:   `{}`,
			probe: func(t *testing.T, st *memStore, _ *bool) {
				if st.accs["a7"].Status != StatusAuthFailed {
					t.Fatalf("403 应终态, got %q", st.accs["a7"].Status)
				}
			},
		},
		{
			name:   "响应缺refresh_token_保留存量RT(RFC6749§6)",
			acc:    Account{ID: "a8", Status: StatusActive, RefreshToken: "rt-precious", ExpiresAt: now.Add(time.Minute)},
			status: 200,
			body:   `{"access_token":"at-new","expires_in":7200}`,
			probe: func(t *testing.T, st *memStore, _ *bool) {
				a := st.accs["a8"]
				if a.AccessToken != "at-new" {
					t.Fatalf("access token 应更新: %+v", a)
				}
				if a.RefreshToken != "rt-precious" {
					t.Fatalf("响应缺 RT 时存量 RT 必须保留(覆写为空=凭据销毁): %+v", a)
				}
			},
		},
		{
			name:   "响应缺expires_in_零回写下轮重探",
			acc:    Account{ID: "a9", Status: StatusActive, RefreshToken: "rt-a9", ExpiresAt: now.Add(time.Minute)},
			status: 200,
			body:   `{"access_token":"at-x"}`,
			probe: func(t *testing.T, st *memStore, _ *bool) {
				a := st.accs["a9"]
				if a.AccessToken != "" || a.RefreshToken != "rt-a9" {
					t.Fatalf("缺 expires_in 不得回写(0 期回写=刷新风暴): %+v", a)
				}
			},
		},
		{
			name:   "刷新400其他错误_重探",
			acc:    Account{ID: "a10", Status: StatusActive, RefreshToken: "rt-a10", ExpiresAt: now.Add(time.Minute)},
			status: 400,
			body:   `{"error":"invalid_client"}`,
			probe: func(t *testing.T, st *memStore, _ *bool) {
				if st.accs["a10"].Status != StatusActive {
					t.Fatalf("400 非 invalid_grant 应保持 active 重探(凭据未撤销), got %q", st.accs["a10"].Status)
				}
			},
		},
		{
			name:   "网络错_保持active下轮重探",
			acc:    Account{ID: "a5", Status: StatusActive, RefreshToken: "rt-a5", ExpiresAt: now.Add(time.Minute)},
			status: 500,
			body:   `{}`,
			probe: func(t *testing.T, st *memStore, _ *bool) {
				if st.accs["a5"].Status != StatusActive {
					t.Fatalf("5xx 应保持 active 重探, got %q", st.accs["a5"].Status)
				}
			},
		},
		{
			name:   "终态账号_跳过零调用",
			acc:    Account{ID: "a6", Status: StatusAuthFailed, RefreshToken: "rt-a6", ExpiresAt: now.Add(time.Minute)},
			status: 200,
			body:   `{"access_token":"x","refresh_token":"x","expires_in":60}`,
			probe: func(t *testing.T, st *memStore, _ *bool) {
				if len(st.calls) != 0 {
					t.Fatalf("终态账号不应产生刷新调用: %v", st.calls)
				}
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := newMemStore(c.acc)
			srv := tokenServer(t, st, c.status, c.body)
			defer srv.Close()
			synced := false
			r, _ := newTestRefresher(t, st, srv, func() { synced = true })
			r.tick(context.Background())
			c.probe(t, st, &synced)
			if !synced {
				t.Fatal("每轮末应回调 sync")
			}
		})
	}
}

// --- helpers ---

func TestRefresher_传输层失败_保持active零回写(t *testing.T) {
	// 5xx 之外的传输层失败(连接拒绝/超时)同归「下轮重探」分支:零回写、状态不动
	st := newMemStore(Account{ID: "a1", Status: StatusActive, RefreshToken: "rt", ExpiresAt: time.Now().Add(time.Minute)})
	srv := tokenServer(t, st, 200, `{}`)
	srv.Close() // 预先拆除:tick 时连接拒绝
	r, _ := newTestRefresher(t, st, srv, func() {})
	r.tick(context.Background())
	a := st.accs["a1"]
	if a.Status != StatusActive || a.AccessToken != "" || a.RefreshToken != "rt" {
		t.Fatalf("传输层失败应零回写保持 active: %+v", a)
	}
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("解析 URL %q: %v", raw, err)
	}
	return u
}
