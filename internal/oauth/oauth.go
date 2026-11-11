// Package oauth 承载订阅账号的 OAuth 凭据机制:授权码+PKCE 授权流、token 端点交互
// 与后台刷新器。凭据存取经 AccountStore 接口注入(装配层接 store),本包不依赖存储实现;
// PKCE 用 stdlib sha256+base64url,随机源可注入供确定性测试。
package oauth

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// Config OAuth 端点声明(装配层从 muniu.yaml `oauth:` 段映射);空段 = 授权流与刷新关闭。
type Config struct {
	AuthorizeURL string
	TokenURL     string
	ClientID     string
	CallbackURL  string
}

// Account 账号凭据投影(刷新器视野;管理面直接走 store)。
type Account struct {
	ID           string
	Status       string
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
}

// 账号状态值域(与 store 侧字面量同形;本包不 import store,装配层负责映射)。
const (
	StatusActive     = "active"
	StatusAuthFailed = "auth_failed"
)

// ErrAccountNotFound 账号不存在(装配层适配 store 哨兵错误)。
var ErrAccountNotFound = errors.New("oauth: 账号不存在")

// ErrStateInvalid state 未知/过期/重放(回调拒绝,不落任何凭据)。
var ErrStateInvalid = errors.New("oauth: state 非法或已使用")

// AccountStore 刷新器的凭据存取面(装配层适配 store 实现)。
type AccountStore interface {
	List() ([]Account, error)
	UpdateTokens(id, accessToken, refreshToken string, expiresAt time.Time) error
	SetStatus(id, status, reason string) error
}

// TokenPair token 端点成功响应。
type TokenPair struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    time.Duration
}

// Flow 授权流:Begin 生成 consent URL(PKCE S256 + 一次性 state),Exchange 换 token。
// pending 仅内存态:重启即弃,授权流重跑无妨,不落库。
type Flow struct {
	cfg Config
	hc  *http.Client
	// rand 随机源可注入(测试确定性);生产为 crypto/rand
	rand func(n int) ([]byte, error)

	mu      sync.Mutex
	pending map[string]pendingAuth
}

type pendingAuth struct {
	verifier  string
	accountID string
	createdAt time.Time
}

// pendingTTL state 有效期:consent 页人工操作的时间预算,过期即弃。
const pendingTTL = 10 * time.Minute

// NewFlow 构造;hc 为 nil 用带超时的缺省 client,rand 为 nil 用 crypto/rand。
func NewFlow(cfg Config, hc *http.Client, randFn func(n int) ([]byte, error)) *Flow {
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Second}
	}
	if randFn == nil {
		randFn = func(n int) ([]byte, error) {
			b := make([]byte, n)
			_, err := rand.Read(b)
			return b, err
		}
	}
	return &Flow{cfg: cfg, hc: hc, rand: randFn, pending: map[string]pendingAuth{}}
}

// b64url 无填充 base64url(PKCE 标准形态)。
func b64url(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// Begin 生成 consent URL;accountID 随 state 绑定,回调时归还(新账号为空串)。
func (f *Flow) Begin(accountID string) (string, error) {
	vb, err := f.rand(32)
	if err != nil {
		return "", fmt.Errorf("oauth: 生成 verifier: %w", err)
	}
	sb, err := f.rand(16)
	if err != nil {
		return "", fmt.Errorf("oauth: 生成 state: %w", err)
	}
	verifier, state := b64url(vb), b64url(sb)
	challengeSum := sha256.Sum256([]byte(verifier))
	challenge := b64url(challengeSum[:])

	f.mu.Lock()
	// 过期项顺带清扫(废弃授权流的 pending 生命周期由 TTL 独立兜底,不依赖 Exchange 弹出)
	for s, p := range f.pending {
		if time.Since(p.createdAt) > pendingTTL {
			delete(f.pending, s)
		}
	}
	f.pending[state] = pendingAuth{verifier: verifier, accountID: accountID, createdAt: time.Now()}
	f.mu.Unlock()

	u, err := url.Parse(f.cfg.AuthorizeURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		// 校验失败回删本 state(Begin 先登记后校验,残留会让 TTL 外的 state 悬挂,评审域二 P3)
		f.mu.Lock()
		delete(f.pending, state)
		f.mu.Unlock()
		return "", fmt.Errorf("oauth: authorize_url 非法: %q", f.cfg.AuthorizeURL)
	}
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", f.cfg.ClientID)
	q.Set("redirect_uri", f.cfg.CallbackURL)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("state", state)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// Exchange 用授权码换 token:state 单次弹出(重放/过期拒绝),S256 校验由上游凭
// code_challenge 完成。返回 token 对与 state 绑定的 accountID。
func (f *Flow) Exchange(ctx context.Context, state, code string) (TokenPair, string, error) {
	f.mu.Lock()
	p, ok := f.pending[state]
	if ok {
		delete(f.pending, state) // 单次弹出:重放在此被拒
	}
	f.mu.Unlock()
	if !ok || time.Since(p.createdAt) > pendingTTL {
		return TokenPair{}, "", ErrStateInvalid
	}
	pair, err := f.tokenRequest(ctx, map[string]any{
		"grant_type":    "authorization_code",
		"code":          code,
		"client_id":     f.cfg.ClientID,
		"redirect_uri":  f.cfg.CallbackURL,
		"code_verifier": p.verifier,
	})
	if err != nil {
		return TokenPair{}, "", err
	}
	return pair, p.accountID, nil
}

// Refresh 用 refresh token 换新凭据对。
func (f *Flow) Refresh(ctx context.Context, refreshToken string) (TokenPair, error) {
	return f.tokenRequest(ctx, map[string]any{
		"grant_type":    "refresh_token",
		"refresh_token": refreshToken,
		"client_id":     f.cfg.ClientID,
	})
}

// TokenError token 端点非 2xx 的结构化错误:Status 供终态判定,GrantType 记 OAuth 错误码。
type TokenError struct {
	Status    int
	GrantType string // RFC 6749 error 字段(invalid_grant 等),可能为空
}

func (e *TokenError) Error() string {
	if e.GrantType != "" {
		return fmt.Sprintf("oauth: token 端点 %d(%s)", e.Status, e.GrantType)
	}
	return fmt.Sprintf("oauth: token 端点 %d", e.Status)
}

// tokenRequest 统一 token 端点交互:JSON 请求体,2xx 解 TokenPair,非 2xx 归 TokenError。
func (f *Flow) tokenRequest(ctx context.Context, body map[string]any) (TokenPair, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return TokenPair{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.cfg.TokenURL, bytes.NewReader(buf))
	if err != nil {
		return TokenPair{}, fmt.Errorf("oauth: 构造 token 请求: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := f.hc.Do(req)
	if err != nil {
		return TokenPair{}, fmt.Errorf("oauth: token 端点请求失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return TokenPair{}, fmt.Errorf("oauth: 读 token 响应: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		te := &TokenError{Status: resp.StatusCode}
		var env struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(raw, &env) == nil {
			te.GrantType = env.Error
		}
		return TokenPair{}, te
	}
	var env struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(raw, &env); err != nil || env.AccessToken == "" {
		return TokenPair{}, fmt.Errorf("oauth: token 响应形态非法(缺 access_token)")
	}
	return TokenPair{
		AccessToken:  env.AccessToken,
		RefreshToken: env.RefreshToken,
		ExpiresIn:    time.Duration(env.ExpiresIn) * time.Second,
	}, nil
}
