// 控制面 HTTP 客户端：统一服务身份头、超时与响应体上限；所有调用可取消。
package control

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Paths 控制面端点路径；X-01 冻结后只需改这里。
type Paths struct {
	Snapshot  string
	Register  string
	Heartbeat string
	Usage     string
	Health    string
	Claim     string
	Ack       string
}

// DefaultPaths 当前消费的端点约定。
func DefaultPaths() Paths {
	return Paths{
		Snapshot:  "/control/v1/snapshot",
		Register:  "/control/v1/registry/instances",
		Heartbeat: "/control/v1/registry/instances/{instance_id}/heartbeat",
		Usage:     "/ingest/usage",
		Health:    "/control/v1/health",
		Claim:     "/control/v1/commands/claim",
		Ack:       "/control/v1/commands/{command_id}/ack",
	}
}

// HeartbeatPath 填充实例 id 的路径段。
func (p Paths) HeartbeatPath(instanceID string) string {
	return strings.ReplaceAll(p.Heartbeat, "{instance_id}", url.PathEscape(instanceID))
}

// AckPath 填充命令 id 的路径段。
func (p Paths) AckPath(commandID string) string {
	return strings.ReplaceAll(p.Ack, "{command_id}", url.PathEscape(commandID))
}

// StatusError 控制面非 2xx 响应；只保留状态码，不回显响应体（可能含内部细节）。
type StatusError struct {
	Status int
}

// Error 错误文案。
func (e *StatusError) Error() string { return fmt.Sprintf("control: 控制面状态 %d", e.Status) }

// Client 最小控制面客户端。
type Client struct {
	BaseURL string
	Token   TokenSource
	HTTP    *http.Client
	Paths   Paths
}

// NewClient 构造客户端；缺省 10s 超时与标准路径。
func NewClient(baseURL string, token TokenSource, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Token:   token,
		HTTP:    &http.Client{Timeout: timeout},
		Paths:   DefaultPaths(),
	}
}

// Get 发起 GET；304 原样返回，由调用方按条件请求语义处理。
func (c *Client) Get(ctx context.Context, path string, headers map[string]string) (int, http.Header, []byte, error) {
	return c.do(ctx, http.MethodGet, path, nil, headers)
}

// Post 发起 JSON POST。
func (c *Client) Post(ctx context.Context, path string, body any) (int, []byte, error) {
	status, _, raw, err := c.do(ctx, http.MethodPost, path, body, nil)
	return status, raw, err
}

// do 统一构造请求并读取响应。
func (c *Client) do(ctx context.Context, method, path string, body any, headers map[string]string) (
	int, http.Header, []byte, error,
) {
	req, err := c.newRequest(ctx, method, path, body, headers)
	if err != nil {
		return 0, nil, nil, err
	}
	resp, err := c.HTTP.Do(req) //nolint:bodyclose // 响应体由 readResponse/parseTokenResponse 关闭
	if err != nil {
		return 0, nil, nil, fmt.Errorf("control: 请求失败: %w", err)
	}
	return readResponse(resp)
}

// newRequest 组装请求：JSON、服务身份 Bearer 与调用方头。
func (c *Client) newRequest(ctx context.Context, method, path string, body any,
	headers map[string]string) (*http.Request, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("control: 序列化请求: %w", err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, reader)
	if err != nil {
		return nil, fmt.Errorf("control: 构造请求: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if c.Token != nil {
		token, err := c.Token.Token(ctx)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req, nil
}

// readResponse 读取响应体（上限 16MiB）并按状态码分流错误。
func readResponse(resp *http.Response) (int, http.Header, []byte, error) {
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return resp.StatusCode, resp.Header, nil, fmt.Errorf("control: 读取响应: %w", err)
	}
	if resp.StatusCode != http.StatusNotModified && (resp.StatusCode < 200 || resp.StatusCode > 299) {
		return resp.StatusCode, resp.Header, raw, &StatusError{Status: resp.StatusCode}
	}
	return resp.StatusCode, resp.Header, raw, nil
}
