// 服务身份令牌源：本地 pre-shared（仅 loopback）或 Keycloak client credentials。
// 令牌只存内存，错误与日志均不回显 secret。
package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// TokenSource 返回访问锦官控制面的 Bearer 令牌。
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// StaticToken 本地 pre-shared 服务令牌；配置校验已限制仅 loopback 使用。
type StaticToken string

// Token 返回静态令牌。
func (s StaticToken) Token(context.Context) (string, error) {
	if strings.TrimSpace(string(s)) == "" {
		return "", errors.New("control: 服务令牌为空")
	}
	return string(s), nil
}

// ControlScopes 机器身份请求的最小 scope 集；锦官按端点 scope 校验。
const ControlScopes = "control:register control:snapshot control:command control:usage"

// ClientCredentials Keycloak 机器身份：client_credentials 短时令牌，缓存至到期前 30s。
type ClientCredentials struct {
	TokenURL     string
	ClientID     string
	ClientSecret string
	Scope        string
	Client       *http.Client

	mu      sync.Mutex
	token   string
	expires time.Time
	now     func() time.Time
}

// Token 取缓存令牌或刷新；失败不缓存。
func (c *ClientCredentials) Token(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	if c.now != nil {
		now = c.now()
	}
	if c.token != "" && now.Before(c.expires) {
		return c.token, nil
	}
	tok, ttl, err := c.fetch(ctx)
	if err != nil {
		return "", err
	}
	c.token = tok
	// 提前 30s 过期，避免临界窗口把刚取到的令牌用成 401；短 TTL 至少保留一半。
	margin := 30 * time.Second
	if ttl <= 2*margin {
		margin = ttl / 2
	}
	c.expires = now.Add(ttl - margin)
	return tok, nil
}

// fetch 执行 client_credentials 请求并解析 access_token/expires_in。
func (c *ClientCredentials) fetch(ctx context.Context) (string, time.Duration, error) {
	req, err := c.tokenRequest(ctx)
	if err != nil {
		return "", 0, err
	}
	client := c.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client.Do(req) //nolint:bodyclose // 响应体由 readResponse/parseTokenResponse 关闭
	if err != nil {
		return "", 0, fmt.Errorf("control: 获取服务令牌失败: %w", err)
	}
	return parseTokenResponse(resp)
}

// tokenRequest 构造 client_credentials 表单请求。
func (c *ClientCredentials) tokenRequest(ctx context.Context) (*http.Request, error) {
	if c.TokenURL == "" || c.ClientID == "" || c.ClientSecret == "" {
		return nil, errors.New("control: client credentials 配置不完整")
	}
	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_id", c.ClientID)
	form.Set("client_secret", c.ClientSecret)
	if c.Scope != "" {
		form.Set("scope", c.Scope)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("control: 构造令牌请求: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req, nil
}

// parseTokenResponse 读取并校验令牌响应。
func parseTokenResponse(resp *http.Response) (string, time.Duration, error) {
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", 0, fmt.Errorf("control: 读取令牌响应: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", 0, fmt.Errorf("control: 令牌端点状态 %d", resp.StatusCode)
	}
	var tr struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tr); err != nil {
		return "", 0, fmt.Errorf("control: 令牌响应非法: %w", err)
	}
	if tr.AccessToken == "" {
		return "", 0, errors.New("control: 令牌响应缺少 access_token")
	}
	ttl := time.Duration(tr.ExpiresIn) * time.Second
	if ttl <= 0 {
		ttl = time.Minute
	}
	return tr.AccessToken, ttl, nil
}
