// Package oidc 是木牛 console 的 OIDC RP 客户端:授权码 + PKCE(S256,与 internal/oauth
// 同一范式)、state/nonce 单次、JWKS 验签与 iss/aud/nonce/时效校验。仅用 stdlib,
// 不引第三方 OIDC 库(依赖白名单制);端点为发现文档驱动,权威不可达时整体失败,
// 由调用方保证本地 break-glass 轨不受影响。
package oidc

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Config RP 声明;Issuer/ClientID/RedirectURL 必填,ClientSecret 空 = 公开客户端(PKCE)。
type Config struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	RedirectURL  string
	Scopes       []string
	StateStore   StateStore // nil = 仅进程内(state 随重启丢失,单副本可用)
}

// Identity 验签通过后的Keycloak身份投影。
type Identity struct {
	Subject     string
	Name        string
	Email       string
	ClientRoles []string
}

// ErrStateInvalid state 未知/过期/重放(回调拒绝,不进行 token 交换)。
var ErrStateInvalid = errors.New("oidc: state 非法或已使用")

// ErrTokenInvalid id_token 验签或声明校验失败。
var ErrTokenInvalid = errors.New("oidc: id_token 校验失败")

// ErrServiceTokenInvalid 服务令牌验签或声明校验失败;调用方统一映射 401,不回传细节。
var ErrServiceTokenInvalid = errors.New("oidc: 服务令牌校验失败")

const (
	pendingTTL     = 10 * time.Minute
	metadataTTL    = 10 * time.Minute
	jwksMinRefresh = 30 * time.Second
	clockLeeway    = 60 * time.Second
)

type pendingAuth struct {
	verifier  string
	nonce     string
	createdAt time.Time
}

type discovery struct {
	Issuer        string   `json:"issuer"`
	AuthorizeURL  string   `json:"authorization_endpoint"`
	TokenURL      string   `json:"token_endpoint"`
	JWKSURL       string   `json:"jwks_uri"`
	EndSessionURL string   `json:"end_session_endpoint"`
	CodeChallenge []string `json:"code_challenge_methods_supported"`
}

// StateStore 持久化登录中 state:多副本/重启不丢请求;实现按 state 哈希存取并清理过期行。
type StateStore interface {
	// Save 写入本次登录材料;ttl 用于顺带清理已过期存量,避免只发起不回调时无限增长。
	Save(ctx context.Context, state, verifier, nonce string, createdAt time.Time, ttl time.Duration) error
	// Take 单次取出并删除;不存在或超过 ttl 返回 ok=false。
	Take(ctx context.Context, state string, now time.Time, ttl time.Duration) (verifier, nonce string, ok bool, err error)
}

type jwkKey struct {
	kid string
	alg string
	key crypto.PublicKey
}

// RelyingParty 无后台循环:发现文档与 JWKS 按需拉取并在内存缓存,重启即弃。
type RelyingParty struct {
	cfg  Config
	hc   *http.Client
	rand func(n int) ([]byte, error)
	now  func() time.Time

	mu        sync.Mutex
	pending   map[string]pendingAuth
	doc       *discovery
	docAt     time.Time
	keys      []jwkKey
	keysAt    time.Time
	keysTried time.Time
}

// New 构造 RP;hc 为 nil 用 10s 超时缺省 client。
func New(cfg Config, hc *http.Client) *RelyingParty {
	if len(cfg.Scopes) == 0 {
		cfg.Scopes = []string{"openid", "profile", "email"}
	}
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	return &RelyingParty{
		cfg: cfg, hc: hc,
		rand: func(n int) ([]byte, error) {
			b := make([]byte, n)
			_, err := rand.Read(b)
			return b, err
		},
		now:     time.Now,
		pending: map[string]pendingAuth{},
	}
}

func b64url(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// RedirectURL 返回声明的回调地址,供同源推导登出回跳等装配级用途。
func (rp *RelyingParty) RedirectURL() string { return rp.cfg.RedirectURL }

// Begin 拉取发现文档并生成授权 URL;state 绑定 verifier 与 nonce,回调单次弹出。
func (rp *RelyingParty) Begin(ctx context.Context) (string, error) {
	doc, err := rp.metadata(ctx)
	if err != nil {
		return "", err
	}
	vb, err := rp.rand(32)
	if err != nil {
		return "", fmt.Errorf("oidc: 生成 verifier: %w", err)
	}
	sb, err := rp.rand(16)
	if err != nil {
		return "", fmt.Errorf("oidc: 生成 state: %w", err)
	}
	nb, err := rp.rand(16)
	if err != nil {
		return "", fmt.Errorf("oidc: 生成 nonce: %w", err)
	}
	verifier, state, nonce := b64url(vb), b64url(sb), b64url(nb)
	challengeSum := sha256.Sum256([]byte(verifier))

	if err := rp.savePending(ctx, state, verifier, nonce); err != nil {
		return "", err
	}

	u, err := url.Parse(doc.AuthorizeURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		rp.dropState(state)
		return "", fmt.Errorf("oidc: authorization_endpoint 非法: %q", doc.AuthorizeURL)
	}
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", rp.cfg.ClientID)
	q.Set("redirect_uri", rp.cfg.RedirectURL)
	q.Set("scope", strings.Join(rp.cfg.Scopes, " "))
	q.Set("state", state)
	q.Set("nonce", nonce)
	q.Set("code_challenge", b64url(challengeSum[:]))
	q.Set("code_challenge_method", "S256")
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// savePending 落 state 与 PKCE 材料:配置了持久化则写库,否则进进程内表并顺带清过期项。
func (rp *RelyingParty) savePending(ctx context.Context, state, verifier, nonce string) error {
	if rp.cfg.StateStore != nil {
		if err := rp.cfg.StateStore.Save(ctx, state, verifier, nonce, rp.now(), pendingTTL); err != nil {
			return fmt.Errorf("oidc: 持久化 state: %w", err)
		}
		return nil
	}
	rp.mu.Lock()
	for s, p := range rp.pending {
		if rp.now().Sub(p.createdAt) > pendingTTL {
			delete(rp.pending, s)
		}
	}
	rp.pending[state] = pendingAuth{verifier: verifier, nonce: nonce, createdAt: rp.now()}
	rp.mu.Unlock()
	return nil
}

// Complete 用授权码换 id_token 并验签;state 单次弹出,重放在此被拒。
func (rp *RelyingParty) Complete(ctx context.Context, state, code string) (Identity, error) {
	verifier, nonce, ok, err := rp.takePending(ctx, state)
	if err != nil {
		return Identity{}, err
	}
	if !ok {
		return Identity{}, ErrStateInvalid
	}
	doc, err := rp.metadata(ctx)
	if err != nil {
		return Identity{}, err
	}
	raw, err := rp.exchange(ctx, doc.TokenURL, verifier, code)
	if err != nil {
		return Identity{}, err
	}
	claims, err := rp.verify(ctx, raw, doc, nonce)
	if err != nil {
		return Identity{}, err
	}
	return identityFromClaims(claims, rp.cfg.ClientID), nil
}

// takePending 单次取出 state 对应的 PKCE 材料;不存在/过期/已用均 ok=false。
func (rp *RelyingParty) takePending(ctx context.Context, state string) (string, string, bool, error) {
	if rp.cfg.StateStore != nil {
		verifier, nonce, ok, err := rp.cfg.StateStore.Take(ctx, state, rp.now(), pendingTTL)
		if err != nil {
			return "", "", false, fmt.Errorf("oidc: 读取 state: %w", err)
		}
		return verifier, nonce, ok, nil
	}
	rp.mu.Lock()
	p, ok := rp.pending[state]
	if ok {
		delete(rp.pending, state)
	}
	rp.mu.Unlock()
	if !ok || rp.now().Sub(p.createdAt) > pendingTTL {
		return "", "", false, nil
	}
	return p.verifier, p.nonce, true, nil
}

// dropState 撤销未能交付给浏览器的 state;持久化实现靠 TTL 惰性清理。
func (rp *RelyingParty) dropState(state string) {
	if rp.cfg.StateStore != nil {
		return
	}
	rp.mu.Lock()
	delete(rp.pending, state)
	rp.mu.Unlock()
}

// EndSessionURL 构造Keycloak RP 发起登出地址;调用方须保证 postLogoutRedirect 已在 client 登记。
func (rp *RelyingParty) EndSessionURL(ctx context.Context, postLogoutRedirect string) (string, error) {
	doc, err := rp.metadata(ctx)
	if err != nil {
		return "", err
	}
	if doc.EndSessionURL == "" {
		return "", errors.New("oidc: 发现文档缺 end_session_endpoint")
	}
	u, err := url.Parse(doc.EndSessionURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("oidc: end_session_endpoint 非法: %q", doc.EndSessionURL)
	}
	q := u.Query()
	q.Set("client_id", rp.cfg.ClientID)
	if postLogoutRedirect != "" {
		q.Set("post_logout_redirect_uri", postLogoutRedirect)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// exchange 标准 token 端点交互:application/x-www-form-urlencoded(RFC 6749)。
func (rp *RelyingParty) exchange(ctx context.Context, tokenURL, verifier, code string) (string, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", rp.cfg.RedirectURL)
	form.Set("client_id", rp.cfg.ClientID)
	form.Set("code_verifier", verifier)
	if rp.cfg.ClientSecret != "" {
		form.Set("client_secret", rp.cfg.ClientSecret)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("oidc: 构造 token 请求: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := rp.hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("oidc: token 端点请求失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("oidc: 读 token 响应: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var env struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(body, &env)
		if env.Error != "" {
			return "", fmt.Errorf("oidc: token 端点 %d(%s)", resp.StatusCode, env.Error)
		}
		return "", fmt.Errorf("oidc: token 端点 %d", resp.StatusCode)
	}
	var env struct {
		IDToken string `json:"id_token"`
	}
	if err := json.Unmarshal(body, &env); err != nil || env.IDToken == "" {
		return "", fmt.Errorf("%w: token 响应缺 id_token", ErrTokenInvalid)
	}
	return env.IDToken, nil
}

// metadata 发现文档:缓存 TTL 内复用;issuer 必须与声明逐字一致(OIDC Discovery 6.3)。
func (rp *RelyingParty) metadata(ctx context.Context) (*discovery, error) {
	rp.mu.Lock()
	if rp.doc != nil && rp.now().Sub(rp.docAt) < metadataTTL {
		doc := rp.doc
		rp.mu.Unlock()
		return doc, nil
	}
	rp.mu.Unlock()

	raw, err := rp.getJSON(ctx, strings.TrimSuffix(rp.cfg.Issuer, "/")+"/.well-known/openid-configuration")
	if err != nil {
		return nil, fmt.Errorf("oidc: 拉取发现文档: %w", err)
	}
	var doc discovery
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("oidc: 发现文档非法: %w", err)
	}
	if doc.Issuer != rp.cfg.Issuer {
		return nil, fmt.Errorf("oidc: 发现文档 issuer 不符: 声明 %q,实际 %q", rp.cfg.Issuer, doc.Issuer)
	}
	if doc.AuthorizeURL == "" || doc.TokenURL == "" || doc.JWKSURL == "" {
		return nil, errors.New("oidc: 发现文档缺 authorization/token/jwks 端点")
	}
	rp.mu.Lock()
	rp.doc, rp.docAt = &doc, rp.now()
	rp.mu.Unlock()
	return &doc, nil
}

func (rp *RelyingParty) getJSON(ctx context.Context, endpoint string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := rp.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}

// verify 解码并校验 id_token:alg 白名单、JWKS 验签、iss/aud/azp/nonce/exp/nbf。
func (rp *RelyingParty) verify(ctx context.Context, raw string, doc *discovery, nonce string) (map[string]any, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("%w: JWT 须为三段", ErrTokenInvalid)
	}
	headerRaw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("%w: 头部解码失败", ErrTokenInvalid)
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal(headerRaw, &header); err != nil {
		return nil, fmt.Errorf("%w: 头部非法", ErrTokenInvalid)
	}
	if !allowedAlg(header.Alg) {
		return nil, fmt.Errorf("%w: 不接受算法 %q", ErrTokenInvalid, header.Alg)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("%w: 签名解码失败", ErrTokenInvalid)
	}
	input := []byte(parts[0] + "." + parts[1])
	if err := rp.verifySignature(ctx, doc.JWKSURL, header.Kid, header.Alg, input, sig); err != nil {
		return nil, err
	}
	payloadRaw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("%w: 载荷解码失败", ErrTokenInvalid)
	}
	var claims map[string]any
	if err := json.Unmarshal(payloadRaw, &claims); err != nil {
		return nil, fmt.Errorf("%w: 载荷非法", ErrTokenInvalid)
	}
	return claims, rp.checkClaims(claims, nonce)
}

// checkClaims 声明校验;时钟偏差容忍 clockLeeway。
func (rp *RelyingParty) checkClaims(claims map[string]any, nonce string) error {
	if iss, _ := claims["iss"].(string); iss != rp.cfg.Issuer {
		return fmt.Errorf("%w: iss 不符", ErrTokenInvalid)
	}
	if err := rp.checkAudience(claims); err != nil {
		return err
	}
	if sub, _ := claims["sub"].(string); sub == "" {
		return fmt.Errorf("%w: 缺 sub", ErrTokenInvalid)
	}
	if err := checkTimeClaims(claims, rp.now()); err != nil {
		return err
	}
	got, _ := claims["nonce"].(string)
	if subtle.ConstantTimeCompare([]byte(got), []byte(nonce)) != 1 {
		return fmt.Errorf("%w: nonce 不符", ErrTokenInvalid)
	}
	return nil
}

// checkAudience aud 必含本客户端;多受众时必须声明 azp=本客户端(防令牌混用)。
func (rp *RelyingParty) checkAudience(claims map[string]any) error {
	if !audienceContains(claims["aud"], rp.cfg.ClientID) {
		return fmt.Errorf("%w: aud 不含本客户端", ErrTokenInvalid)
	}
	auds, ok := claims["aud"].([]any)
	if ok && len(auds) > 1 {
		if azp, _ := claims["azp"].(string); azp != rp.cfg.ClientID {
			return fmt.Errorf("%w: 多受众令牌缺 azp", ErrTokenInvalid)
		}
	}
	return nil
}

// ServiceTokenConfig 服务只读 API 的 Keycloak 令牌校验声明。
type ServiceTokenConfig struct {
	Issuer         string
	Audience       string   // 目标服务受众,如 muniu
	AllowedClients []string // 允许的 azp 白名单;空即全部拒绝
	RequiredScope  string   // 必含 scope,如 service:read
}

// ServiceVerifier 校验 Keycloak client credentials access token:
// 发现文档 + JWKS 按 kid 验签,校验 iss/aud/exp/nbf/azp/scope。仅接受 RS256。
type ServiceVerifier struct {
	rp       *RelyingParty
	audience string
	allowed  map[string]struct{}
	scope    string
}

// NewServiceVerifier 构造服务令牌校验器;网络端点按需拉取并缓存。
func NewServiceVerifier(cfg ServiceTokenConfig, hc *http.Client) *ServiceVerifier {
	allowed := make(map[string]struct{}, len(cfg.AllowedClients))
	for _, client := range cfg.AllowedClients {
		if client = strings.TrimSpace(client); client != "" {
			allowed[client] = struct{}{}
		}
	}
	return &ServiceVerifier{
		rp:       New(Config{Issuer: cfg.Issuer}, hc),
		audience: cfg.Audience,
		allowed:  allowed,
		scope:    cfg.RequiredScope,
	}
}

// Verify 返回通过校验的 client id;失败统一 ErrServiceTokenInvalid。
func (v *ServiceVerifier) Verify(ctx context.Context, raw string) (string, error) {
	claims, err := v.claims(ctx, strings.TrimSpace(raw))
	if err != nil {
		return "", err
	}
	clientID, _ := claims["azp"].(string)
	if clientID == "" {
		clientID, _ = claims["client_id"].(string)
	}
	if _, ok := v.allowed[clientID]; !ok {
		return "", fmt.Errorf("%w: 调用方不在读者白名单", ErrServiceTokenInvalid)
	}
	scope, _ := claims["scope"].(string)
	if !scopeContains(scope, v.scope) {
		return "", fmt.Errorf("%w: 缺少 scope %s", ErrServiceTokenInvalid, v.scope)
	}
	return clientID, nil
}

// claims 验签并校验服务令牌声明;不校验 nonce(非 id_token)。
func (v *ServiceVerifier) claims(ctx context.Context, raw string) (map[string]any, error) {
	parsed, err := parseServiceToken(raw)
	if err != nil {
		return nil, err
	}
	doc, err := v.rp.metadata(ctx)
	if err != nil {
		return nil, wrapServiceErr(err)
	}
	if err := v.rp.verifySignature(ctx, doc.JWKSURL, parsed.kid, parsed.alg, parsed.input, parsed.sig); err != nil {
		return nil, wrapServiceErr(err)
	}
	claims, err := decodeServiceClaims(parsed.payload)
	if err != nil {
		return nil, err
	}
	if err := v.checkClaims(claims); err != nil {
		return nil, err
	}
	return claims, nil
}

type serviceToken struct {
	alg     string
	kid     string
	input   []byte
	sig     []byte
	payload []byte
}

// parseServiceToken 解析头部/签名/载荷;只接受 RS256。
func parseServiceToken(raw string) (serviceToken, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return serviceToken{}, fmt.Errorf("%w: JWT 须为三段", ErrServiceTokenInvalid)
	}
	headerRaw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return serviceToken{}, fmt.Errorf("%w: 头部解码失败", ErrServiceTokenInvalid)
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal(headerRaw, &header); err != nil {
		return serviceToken{}, fmt.Errorf("%w: 头部非法", ErrServiceTokenInvalid)
	}
	if header.Alg != "RS256" {
		return serviceToken{}, fmt.Errorf("%w: 不接受算法 %q", ErrServiceTokenInvalid, header.Alg)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return serviceToken{}, fmt.Errorf("%w: 签名解码失败", ErrServiceTokenInvalid)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return serviceToken{}, fmt.Errorf("%w: 载荷解码失败", ErrServiceTokenInvalid)
	}
	return serviceToken{
		alg: header.Alg, kid: header.Kid,
		input: []byte(parts[0] + "." + parts[1]), sig: sig, payload: payload,
	}, nil
}

func decodeServiceClaims(payload []byte) (map[string]any, error) {
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, fmt.Errorf("%w: 载荷非法", ErrServiceTokenInvalid)
	}
	return claims, nil
}

func (v *ServiceVerifier) checkClaims(claims map[string]any) error {
	if iss, _ := claims["iss"].(string); iss != v.rp.cfg.Issuer {
		return fmt.Errorf("%w: iss 不符", ErrServiceTokenInvalid)
	}
	if !audienceContains(claims["aud"], v.audience) {
		return fmt.Errorf("%w: aud 不含 %s", ErrServiceTokenInvalid, v.audience)
	}
	if err := checkTimeClaims(claims, v.rp.now()); err != nil {
		return wrapServiceErr(err)
	}
	return nil
}

// wrapServiceErr 保留底层错误链并统一归入服务令牌失败。
func wrapServiceErr(err error) error {
	return fmt.Errorf("%w: %w", ErrServiceTokenInvalid, err)
}

func scopeContains(granted, required string) bool {
	if required == "" {
		return true
	}
	for _, scope := range strings.Fields(granted) {
		if scope == required {
			return true
		}
	}
	return false
}

// checkTimeClaims exp 必填且未过期,nbf 不晚于容差窗口。
func checkTimeClaims(claims map[string]any, now time.Time) error {
	if exp, ok := numClaim(claims["exp"]); !ok || now.After(time.Unix(exp, 0).Add(clockLeeway)) {
		return fmt.Errorf("%w: 令牌已过期或缺 exp", ErrTokenInvalid)
	}
	if nbf, ok := numClaim(claims["nbf"]); ok && now.Add(clockLeeway).Before(time.Unix(nbf, 0)) {
		return fmt.Errorf("%w: 令牌尚未生效", ErrTokenInvalid)
	}
	return nil
}

func allowedAlg(alg string) bool {
	switch alg {
	case "RS256", "RS384", "RS512", "PS256", "PS384", "PS512", "ES256", "ES384", "ES512":
		return true
	}
	return false
}

func numClaim(v any) (int64, bool) {
	f, ok := v.(float64)
	if !ok {
		return 0, false
	}
	return int64(f), true
}

func audienceContains(aud any, clientID string) bool {
	switch v := aud.(type) {
	case string:
		return v == clientID
	case []any:
		for _, item := range v {
			if s, _ := item.(string); s == clientID {
				return true
			}
		}
	}
	return false
}

// verifySignature 按 kid 选键;kid 未命中时强制刷新一次 JWKS(限速),仍无则拒绝。
func (rp *RelyingParty) verifySignature(ctx context.Context, jwksURL, kid, alg string, input, sig []byte) error {
	keys, err := rp.jwks(ctx, jwksURL, false)
	if err != nil {
		return err
	}
	if rp.tryKeys(keys, kid, alg, input, sig) {
		return nil
	}
	rp.mu.Lock()
	throttled := rp.now().Sub(rp.keysTried) < jwksMinRefresh
	rp.mu.Unlock()
	if !throttled {
		keys, err = rp.jwks(ctx, jwksURL, true)
		if err != nil {
			return err
		}
		if rp.tryKeys(keys, kid, alg, input, sig) {
			return nil
		}
	}
	return fmt.Errorf("%w: 签名校验失败", ErrTokenInvalid)
}

func (rp *RelyingParty) tryKeys(keys []jwkKey, kid, alg string, input, sig []byte) bool {
	for _, k := range keys {
		if kid != "" && k.kid != kid {
			continue
		}
		if !algMatchesKey(alg, k.key) {
			continue
		}
		if verifyWith(k.key, alg, input, sig) {
			return true
		}
	}
	return false
}

// jwks 返回缓存键集;force 时受 jwksMinRefresh 限速刷新(未知 kid 轮换场景)。
func (rp *RelyingParty) jwks(ctx context.Context, jwksURL string, force bool) ([]jwkKey, error) {
	rp.mu.Lock()
	fresh := rp.now().Sub(rp.keysAt) < metadataTTL
	if !force && fresh && len(rp.keys) > 0 {
		keys := rp.keys
		rp.mu.Unlock()
		return keys, nil
	}
	if force {
		// 只对强制刷新限速；首次常规拉取不应阻塞紧随其后的轮换刷新。
		rp.keysTried = rp.now()
	}
	rp.mu.Unlock()

	raw, err := rp.getJSON(ctx, jwksURL)
	if err != nil {
		return nil, fmt.Errorf("oidc: 拉取 JWKS: %w", err)
	}
	keys := parseJWKS(raw)
	if len(keys) == 0 {
		return nil, fmt.Errorf("%w: JWKS 无可用公钥", ErrTokenInvalid)
	}
	rp.mu.Lock()
	rp.keys, rp.keysAt = keys, rp.now()
	rp.mu.Unlock()
	return keys, nil
}

// jwkEntry JWKS 单键条目;解码失败不是安全事件,由 publicKey 跳过。
type jwkEntry struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Alg string `json:"alg"`
	Use string `json:"use"`
	N   string `json:"n"`
	E   string `json:"e"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

// parseJWKS 解析 RSA 与 EC(P-256/384/521)公钥;其余 kty 跳过。
func parseJWKS(raw []byte) []jwkKey {
	var set struct {
		Keys []jwkEntry `json:"keys"`
	}
	if err := json.Unmarshal(raw, &set); err != nil {
		return nil
	}
	var out []jwkKey
	for _, k := range set.Keys {
		if k.Use != "" && k.Use != "sig" {
			continue
		}
		if pub, ok := k.publicKey(); ok {
			out = append(out, jwkKey{kid: k.Kid, alg: k.Alg, key: pub})
		}
	}
	return out
}

func (k jwkEntry) publicKey() (crypto.PublicKey, bool) {
	switch k.Kty {
	case "RSA":
		return k.rsaPublicKey()
	case "EC":
		return k.ecPublicKey()
	}
	return nil, false
}

func (k jwkEntry) rsaPublicKey() (crypto.PublicKey, bool) {
	nb, err1 := base64.RawURLEncoding.DecodeString(k.N)
	eb, err2 := base64.RawURLEncoding.DecodeString(k.E)
	if err1 != nil || err2 != nil || len(nb) == 0 || len(eb) == 0 {
		return nil, false
	}
	return &rsa.PublicKey{N: new(big.Int).SetBytes(nb), E: int(new(big.Int).SetBytes(eb).Int64())}, true
}

// ecPublicKey 坐标左补齐成未压缩点后经 stdlib 构造(Go 1.27 裸坐标构造已弃用)。
func (k jwkEntry) ecPublicKey() (crypto.PublicKey, bool) {
	curve := ecCurve(k.Crv)
	if curve == nil {
		return nil, false
	}
	xb, err1 := base64.RawURLEncoding.DecodeString(k.X)
	yb, err2 := base64.RawURLEncoding.DecodeString(k.Y)
	if err1 != nil || err2 != nil {
		return nil, false
	}
	size := (curve.Params().BitSize + 7) / 8
	if len(xb) > size || len(yb) > size {
		return nil, false
	}
	buf := make([]byte, 0, 1+2*size)
	buf = append(buf, 4)
	buf = appendLeftPadded(buf, xb, size)
	buf = appendLeftPadded(buf, yb, size)
	pub, err := ecdsa.ParseUncompressedPublicKey(curve, buf)
	if err != nil {
		return nil, false
	}
	return pub, true
}

func appendLeftPadded(dst, src []byte, size int) []byte {
	for i := size - len(src); i > 0; i-- {
		dst = append(dst, 0)
	}
	return append(dst, src...)
}

func ecCurve(crv string) elliptic.Curve {
	switch crv {
	case "P-256":
		return elliptic.P256()
	case "P-384":
		return elliptic.P384()
	case "P-521":
		return elliptic.P521()
	}
	return nil
}

func algMatchesKey(alg string, key crypto.PublicKey) bool {
	switch {
	case strings.HasPrefix(alg, "RS"), strings.HasPrefix(alg, "PS"):
		_, ok := key.(*rsa.PublicKey)
		return ok
	case strings.HasPrefix(alg, "ES"):
		_, ok := key.(*ecdsa.PublicKey)
		return ok
	}
	return false
}

// verifyWith 按算法分流验签;不支持或长度不符一律失败(不 panic)。
func verifyWith(key crypto.PublicKey, alg string, input, sig []byte) bool {
	hash, digest := hashFor(alg, input)
	if hash == 0 {
		return false
	}
	switch k := key.(type) {
	case *rsa.PublicKey:
		if strings.HasPrefix(alg, "PS") {
			return rsa.VerifyPSS(k, hash, digest, sig, &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthAuto}) == nil
		}
		return rsa.VerifyPKCS1v15(k, hash, digest, sig) == nil
	case *ecdsa.PublicKey:
		size := (k.Curve.Params().BitSize + 7) / 8
		if len(sig) != 2*size {
			return false
		}
		r := new(big.Int).SetBytes(sig[:size])
		s := new(big.Int).SetBytes(sig[size:])
		return ecdsa.Verify(k, digest, r, s)
	}
	return false
}

func hashFor(alg string, input []byte) (crypto.Hash, []byte) {
	switch alg {
	case "RS256", "PS256", "ES256":
		sum := sha256.Sum256(input)
		return crypto.SHA256, sum[:]
	case "RS384", "PS384", "ES384":
		sum := sha512.Sum384(input)
		return crypto.SHA384, sum[:]
	case "RS512", "PS512", "ES512":
		sum := sha512.Sum512(input)
		return crypto.SHA512, sum[:]
	}
	return 0, nil
}

// identityFromClaims 实名优先 name,其次 preferred_username/email,兜底 sub;
// ClientRoles 只取本 client 的 resource_access roles,其它 client 与 realm roles 忽略。
func identityFromClaims(claims map[string]any, clientID string) Identity {
	str := func(key string) string {
		s, _ := claims[key].(string)
		return strings.TrimSpace(s)
	}
	name := str("name")
	if name == "" {
		name = str("preferred_username")
	}
	if name == "" {
		name = str("email")
	}
	sub := str("sub")
	if name == "" {
		name = sub
	}
	return Identity{Subject: sub, Name: name, Email: str("email"),
		ClientRoles: clientRoles(claims, clientID)}
}

// clientRoles 从 resource_access[clientID].roles 提取字符串角色;缺失/类型不符返回 nil。
func clientRoles(claims map[string]any, clientID string) []string {
	ra, ok := claims["resource_access"].(map[string]any)
	if !ok {
		return nil
	}
	client, ok := ra[clientID].(map[string]any)
	if !ok {
		return nil
	}
	raw, ok := client["roles"].([]any)
	if !ok {
		return nil
	}
	roles := make([]string, 0, len(raw))
	for _, v := range raw {
		if role, ok := v.(string); ok {
			roles = append(roles, role)
		}
	}
	return roles
}
