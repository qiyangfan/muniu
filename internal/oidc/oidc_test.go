package oidc

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeIdP 最小 OIDC 权威:发现文档、JWKS、token 端点;私钥/签名字段可逐用例篡改。
type fakeIdP struct {
	t      *testing.T
	srv    *httptest.Server
	key    *rsa.PrivateKey
	issued time.Time

	mu       sync.Mutex
	tokenReq url.Values // 最近一次 token 端点收到表单
	claims   map[string]any
	alg      string
	useKid   string
	key2     *rsa.PrivateKey
	jwksPub  *rsa.PublicKey // JWKS 公布的公钥;与签名私钥分离以测篡改
}

func newFakeIdP(t *testing.T) *fakeIdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	idp := &fakeIdP{t: t, key: key, jwksPub: &key.PublicKey, issued: time.Now(), alg: "RS256", useKid: "kid-1"}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                 idp.issuer(),
			"authorization_endpoint": idp.srv.URL + "/authorize",
			"token_endpoint":         idp.srv.URL + "/token",
			"jwks_uri":               idp.srv.URL + "/jwks",
			// 标准发现文档为数组;曾误声明为 string 导致解析失败(回归保护)。
			"code_challenge_methods_supported": []string{"S256"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		keys := []map[string]any{rsaJWK("kid-1", idp.jwksPub)}
		idp.mu.Lock()
		rotated := idp.key2
		idp.mu.Unlock()
		if rotated != nil {
			keys = append(keys, rsaJWK("kid-2", &rotated.PublicKey))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": keys})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		idp.mu.Lock()
		idp.tokenReq = r.PostForm
		claims := idp.claims
		alg := idp.alg
		idp.mu.Unlock()
		token := idp.sign(claims, alg)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "at", "token_type": "Bearer", "expires_in": 3600, "id_token": token,
		})
	})
	idp.srv = httptest.NewServer(mux)
	t.Cleanup(idp.srv.Close)
	return idp
}

func (f *fakeIdP) issuer() string { return f.srv.URL }

func rsaJWK(kid string, pub *rsa.PublicKey) map[string]any {
	return map[string]any{
		"kty": "RSA", "kid": kid, "alg": "RS256", "use": "sig",
		"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
	}
}

func (f *fakeIdP) sign(claims map[string]any, alg string) string {
	f.t.Helper()
	header := map[string]any{"alg": alg, "typ": "JWT", "kid": f.useKid}
	if alg == "none" {
		header = map[string]any{"alg": "none"}
	}
	hb, _ := json.Marshal(header)
	cb, _ := json.Marshal(claims)
	signingInput := base64.RawURLEncoding.EncodeToString(hb) + "." + base64.RawURLEncoding.EncodeToString(cb)
	if alg == "none" {
		return signingInput + "."
	}
	sum := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, sum[:])
	if err != nil {
		f.t.Fatal(err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func (f *fakeIdP) setClaims(c map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.claims = c
}

func (f *fakeIdP) tokenForm() url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tokenReq
}

// rpFor 构造指向假权威的 RP;claims 为 id_token 载荷模板,nonce 由 Begin 的真实值回填。
func rpFor(t *testing.T, idp *fakeIdP, claims map[string]any) (*RelyingParty, string) {
	t.Helper()
	rp := New(Config{
		Issuer: idp.issuer(), ClientID: "muniu-console",
		RedirectURL: "http://127.0.0.1:8787/admin/v1/auth/oidc/callback",
	}, idp.srv.Client())
	authURL, err := rp.Begin(context.Background())
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	full := map[string]any{
		"iss": idp.issuer(), "aud": "muniu-console", "sub": "subject-1",
		"name": "张三", "email": "zhangsan@example.com",
		"exp": idp.issued.Add(time.Hour).Unix(), "iat": idp.issued.Unix(),
		"nonce": u.Query().Get("nonce"),
	}
	for k, v := range claims {
		full[k] = v
	}
	idp.setClaims(full)
	return rp, u.Query().Get("state")
}

func TestBegin_授权URL携带PKCE与nonce(t *testing.T) {
	idp := newFakeIdP(t)
	rp := New(Config{Issuer: idp.issuer(), ClientID: "muniu-console",
		RedirectURL: "http://127.0.0.1:8787/admin/v1/auth/oidc/callback"}, idp.srv.Client())
	authURL, err := rp.Begin(context.Background())
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	u, _ := url.Parse(authURL)
	q := u.Query()
	if q.Get("response_type") != "code" || q.Get("client_id") != "muniu-console" {
		t.Fatalf("授权参数缺失: %s", authURL)
	}
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
		t.Fatalf("缺 PKCE S256: %s", authURL)
	}
	if q.Get("state") == "" || q.Get("nonce") == "" {
		t.Fatalf("缺 state/nonce: %s", authURL)
	}
	if !strings.Contains(q.Get("scope"), "openid") {
		t.Fatalf("scope 必含 openid: %q", q.Get("scope"))
	}
}

func TestComplete_PKCE与验签全链(t *testing.T) {
	idp := newFakeIdP(t)
	rp, state := rpFor(t, idp, nil)
	id, err := rp.Complete(context.Background(), state, "auth-code-1")
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if id.Subject != "subject-1" || id.Name != "张三" {
		t.Fatalf("身份映射错误: %+v", id)
	}
	form := idp.tokenForm()
	if form.Get("grant_type") != "authorization_code" || form.Get("code") != "auth-code-1" {
		t.Fatalf("token 请求参数错误: %v", form)
	}
	sum := sha256.Sum256([]byte(form.Get("code_verifier")))
	if base64.RawURLEncoding.EncodeToString(sum[:]) == "" || form.Get("code_verifier") == "" {
		t.Fatalf("缺 code_verifier: %v", form)
	}
}

func TestComplete_state单次使用(t *testing.T) {
	idp := newFakeIdP(t)
	rp, state := rpFor(t, idp, nil)
	if _, err := rp.Complete(context.Background(), state, "code"); err != nil {
		t.Fatalf("首次 Complete: %v", err)
	}
	if _, err := rp.Complete(context.Background(), state, "code"); err == nil {
		t.Fatal("state 重放必须拒绝")
	}
	if _, err := rp.Complete(context.Background(), "unknown-state", "code"); err == nil {
		t.Fatal("未知 state 必须拒绝")
	}
}

func TestComplete_拒绝非法令牌(t *testing.T) {
	cases := []struct {
		name   string
		claims map[string]any
		alg    string
	}{
		{name: "过期", claims: map[string]any{"exp": time.Now().Add(-time.Hour).Unix()}},
		{name: "受众不符", claims: map[string]any{"aud": "other-client"}},
		{name: "签发方不符", claims: map[string]any{"iss": "https://evil.example"}},
		{name: "nonce 不符", claims: map[string]any{"nonce": "forged"}},
		{name: "alg none", claims: nil, alg: "none"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			idp := newFakeIdP(t)
			if tc.alg != "" {
				idp.alg = tc.alg
			}
			rp, state := rpFor(t, idp, tc.claims)
			if _, err := rp.Complete(context.Background(), state, "code"); err == nil {
				t.Fatal("非法 id_token 必须拒绝")
			}
		})
	}
}

func TestComplete_签名篡改拒绝(t *testing.T) {
	idp := newFakeIdP(t)
	rp, state := rpFor(t, idp, nil)
	// 换私钥签同一组声明:kid 命中但验签失败
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	idp.mu.Lock()
	idp.key = other
	idp.mu.Unlock()
	if _, err := rp.Complete(context.Background(), state, "code"); err == nil {
		t.Fatal("签名不匹配必须拒绝")
	}
}

func TestComplete_未知kid触发JWKS刷新(t *testing.T) {
	idp := newFakeIdP(t)
	rp, state := rpFor(t, idp, nil)
	rotated, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	idp.mu.Lock()
	idp.key2 = rotated
	idp.key = rotated
	idp.useKid = "kid-2"
	idp.mu.Unlock()
	if _, err := rp.Complete(context.Background(), state, "code"); err != nil {
		t.Fatalf("密钥轮换后应经 JWKS 刷新通过: %v", err)
	}
}

func TestComplete_权威不可达(t *testing.T) {
	idp := newFakeIdP(t)
	rp, state := rpFor(t, idp, nil)
	idp.srv.Close()
	if _, err := rp.Complete(context.Background(), state, "code"); err == nil {
		t.Fatal("权威不可达必须返回错误")
	}
}

func TestBegin_发现端点不可达(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	issuer := srv.URL
	srv.Close()
	rp := New(Config{Issuer: issuer, ClientID: "c", RedirectURL: "http://127.0.0.1/cb"}, nil)
	if _, err := rp.Begin(context.Background()); err == nil {
		t.Fatal("发现端点不可达必须返回错误")
	}
}

func TestNew_EC公钥解析分支(t *testing.T) {
	// ECDSA 公钥解析分支:JWKS 含 EC 键时 RSA 令牌不受影响(键选择按 alg 过滤)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := key.PublicKey.Bytes() // SEC1 未压缩点:0x04 || X || Y
	if err != nil {
		t.Fatal(err)
	}
	size := (key.Curve.Params().BitSize + 7) / 8
	jwk := map[string]any{
		"kty": "EC", "kid": "ec-1", "crv": "P-256",
		"x": base64.RawURLEncoding.EncodeToString(raw[1 : 1+size]),
		"y": base64.RawURLEncoding.EncodeToString(raw[1+size : 1+2*size]),
	}
	keys := parseJWKS([]byte(`{"keys":[` + mustJSON(jwk) + `]}`))
	if len(keys) != 1 || keys[0].kid != "ec-1" || keys[0].key == nil {
		t.Fatalf("EC 公钥解析失败: %+v", keys)
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
