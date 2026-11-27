// 快照验签公钥来源：锦官 JWKS 按 kid 缓存，静态 PEM 仅作显式 pin。
package control

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	jwksTTL          = 5 * time.Minute
	jwksForceRefresh = 30 * time.Second
	jwksMaxBytes     = 1 << 20
)

// KeySource 按快照签名 kid 解析 RSA 公钥；kid 为空时返回全部已发布公钥。
type KeySource interface {
	Key(ctx context.Context, keyID string) (*rsa.PublicKey, error)
	Keys(ctx context.Context) ([]*rsa.PublicKey, error)
}

// StaticKeySource 显式 pin 单公钥，不随锦官轮换变化。
type StaticKeySource struct {
	PublicKey *rsa.PublicKey
}

// Key 返回静态 pin；kid 仅用于契约提示，不参与选择。
func (s StaticKeySource) Key(_ context.Context, _ string) (*rsa.PublicKey, error) {
	if s.PublicKey == nil {
		return nil, errors.New("control: 快照验签公钥未配置")
	}
	return s.PublicKey, nil
}

// Keys 返回唯一静态公钥。
func (s StaticKeySource) Keys(ctx context.Context) ([]*rsa.PublicKey, error) {
	key, err := s.Key(ctx, "")
	if err != nil {
		return nil, err
	}
	return []*rsa.PublicKey{key}, nil
}

// JWKSKeySource 拉取并缓存锦官 JWKS；未知 kid 触发限速强制刷新。
type JWKSKeySource struct {
	URL     string
	Client  *http.Client
	Timeout time.Duration

	mu       sync.Mutex
	keys     map[string]*rsa.PublicKey
	loadedAt time.Time
	forcedAt time.Time
}

// NewJWKSKeySource 构造 JWKS 公钥源；缺省 10 秒超时。
func NewJWKSKeySource(url string) *JWKSKeySource {
	return &JWKSKeySource{URL: url, Timeout: 10 * time.Second}
}

// Key 按 kid 选公钥；缓存未命中时强制刷新一次。
func (s *JWKSKeySource) Key(ctx context.Context, keyID string) (*rsa.PublicKey, error) {
	if strings.TrimSpace(keyID) == "" {
		return nil, errors.New("control: 快照缺少 X-Jinguan-Key-Id")
	}
	keys, err := s.load(ctx, false)
	if err != nil {
		return nil, err
	}
	if key, ok := keys[keyID]; ok {
		return key, nil
	}
	keys, err = s.load(ctx, true)
	if err != nil {
		return nil, err
	}
	key, ok := keys[keyID]
	if !ok {
		return nil, fmt.Errorf("control: 快照签名 kid 未知: %s", keyID)
	}
	return key, nil
}

// Keys 返回全部已发布公钥，供无 kid 的 last-known 校验。
func (s *JWKSKeySource) Keys(ctx context.Context) ([]*rsa.PublicKey, error) {
	keys, err := s.load(ctx, false)
	if err != nil {
		return nil, err
	}
	out := make([]*rsa.PublicKey, 0, len(keys))
	for _, key := range keys {
		out = append(out, key)
	}
	return out, nil
}

func (s *JWKSKeySource) load(ctx context.Context, force bool) (map[string]*rsa.PublicKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if s.keys != nil && !force && now.Sub(s.loadedAt) < jwksTTL {
		return s.keys, nil
	}
	if s.keys != nil && force && now.Sub(s.forcedAt) < jwksForceRefresh {
		return s.keys, nil
	}
	if force {
		s.forcedAt = now
	}
	fetched, err := s.fetch(ctx)
	if err != nil {
		if s.keys != nil {
			return s.keys, nil
		}
		return nil, err
	}
	s.keys = fetched
	s.loadedAt = now
	return fetched, nil
}

func (s *JWKSKeySource) fetch(ctx context.Context) (map[string]*rsa.PublicKey, error) {
	client := s.Client
	if client == nil {
		client = &http.Client{
			Timeout: s.Timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL, nil)
	if err != nil {
		return nil, fmt.Errorf("control: 构造 JWKS 请求: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("control: 拉取 JWKS: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("control: JWKS 返回 %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, jwksMaxBytes))
	if err != nil {
		return nil, fmt.Errorf("control: 读取 JWKS: %w", err)
	}
	return parseJWKS(raw)
}

type jwksDocument struct {
	Keys []jwksEntry `json:"keys"`
}

type jwksEntry struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	N   string `json:"n"`
	E   string `json:"e"`
}

func parseJWKS(raw []byte) (map[string]*rsa.PublicKey, error) {
	var doc jwksDocument
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("control: JWKS JSON 非法: %w", err)
	}
	keys := make(map[string]*rsa.PublicKey, len(doc.Keys))
	for _, entry := range doc.Keys {
		if entry.Kty != "RSA" || strings.TrimSpace(entry.Kid) == "" {
			continue
		}
		key, err := entry.publicKey()
		if err != nil {
			return nil, err
		}
		keys[entry.Kid] = key
	}
	if len(keys) == 0 {
		return nil, errors.New("control: JWKS 没有可用的 RSA 公钥")
	}
	return keys, nil
}

func (e jwksEntry) publicKey() (*rsa.PublicKey, error) {
	nBytes, err := base64.RawURLEncoding.DecodeString(e.N)
	if err != nil || len(nBytes) == 0 {
		return nil, fmt.Errorf("control: JWKS kid=%s 的 n 非法", e.Kid)
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(e.E)
	if err != nil || len(eBytes) == 0 {
		return nil, fmt.Errorf("control: JWKS kid=%s 的 e 非法", e.Kid)
	}
	modulus := new(big.Int).SetBytes(nBytes)
	exponent := new(big.Int).SetBytes(eBytes)
	if modulus.BitLen() < 2048 || !exponent.IsInt64() {
		return nil, fmt.Errorf("control: JWKS kid=%s 的 RSA 公钥强度非法", e.Kid)
	}
	return &rsa.PublicKey{N: modulus, E: int(exponent.Int64())}, nil
}
