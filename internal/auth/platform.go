// 自校验平台 key：HMAC 签名载体，网关离线验真，不查任何库。
// 形态为 mk2-<base64url(payload)>.<base64url(tag128)>，
// payload = "pkey.v1|<tenant_id>|<key_id>|<expires_unix>|<rpm>|<tpm>|<daily_quota_credits>"。
// 限额随载体下发，签发时定、换发生效，控制面失联时鉴权与限额仍可用；吊销经版本化名单同步收敛。
package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// PlatformKeyPrefix 平台 key 前缀，与查库 key 的 mk- 并存路由。
const PlatformKeyPrefix = "mk2-"

// platformPayloadVersion 载体版本，格式演进时递增并拒绝旧版本。
const platformPayloadVersion = "pkey.v1"

// platformTagBytes HMAC 截断长度 128bit，防碰撞充分。
const platformTagBytes = 16

// ErrPlatformKey 平台 key 无效，篡改、过期、格式非法或未知密钥，对外统一归 401。
var ErrPlatformKey = errors.New("auth: 平台 key 无效")

// PlatformClaims 平台 key 载体字段，限额签发时定、换发生效。
type PlatformClaims struct {
	TenantID          string
	KeyID             string
	ExpiresUnix       int64
	RPMLimit          int64
	TPMLimit          int64
	DailyQuotaCredits int64
}

// FormatPlatformKey 签发平台 key，控制面产品有同构实现，此处供测试与未来本地签发。
func FormatPlatformKey(c PlatformClaims, secret []byte) (string, error) {
	if c.TenantID == "" || c.KeyID == "" || c.ExpiresUnix <= 0 {
		return "", fmt.Errorf("auth: 平台 key 载体字段不完整")
	}
	if strings.ContainsAny(c.TenantID+c.KeyID, "|") {
		return "", fmt.Errorf("auth: 标识不得包含分隔符 |")
	}
	if c.RPMLimit < 0 || c.TPMLimit < 0 || c.DailyQuotaCredits < 0 {
		return "", fmt.Errorf("auth: 限额不可为负(0=不限)")
	}
	if len(secret) < 16 {
		return "", fmt.Errorf("auth: 平台 key 签名密钥过短(<16 字节)")
	}
	payload := platformPayload(c)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(payload))
	tag := mac.Sum(nil)[:platformTagBytes]
	return PlatformKeyPrefix + base64.RawURLEncoding.EncodeToString([]byte(payload)) +
		"." + base64.RawURLEncoding.EncodeToString(tag), nil
}

// ParsePlatformKey 验真：任一密钥验通过即有效，以支持密钥轮换；过期、篡改、格式非法一律 ErrPlatformKey。
func ParsePlatformKey(key string, secrets [][]byte, now time.Time) (*PlatformClaims, error) {
	bad := func(err error) (*PlatformClaims, error) { return nil, fmt.Errorf("%w: %v", ErrPlatformKey, err) }
	if !strings.HasPrefix(key, PlatformKeyPrefix) || len(secrets) == 0 {
		return bad(errors.New("形态不符"))
	}
	rest := strings.TrimPrefix(key, PlatformKeyPrefix)
	payloadB64, tagB64, ok := strings.Cut(rest, ".")
	if !ok {
		return bad(errors.New("缺少签名段"))
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(payloadB64)
	if err != nil {
		return bad(errors.New("载体非 base64url"))
	}
	tag, err := base64.RawURLEncoding.DecodeString(tagB64)
	if err != nil || len(tag) != platformTagBytes {
		return bad(errors.New("签名长度不符"))
	}
	valid := false
	for _, secret := range secrets {
		mac := hmac.New(sha256.New, secret)
		mac.Write(payloadBytes)
		if hmac.Equal(tag, mac.Sum(nil)[:platformTagBytes]) {
			valid = true
			break
		}
	}
	if !valid {
		return bad(errors.New("签名验真失败"))
	}
	claims, err := parsePlatformPayload(string(payloadBytes))
	if err != nil {
		return bad(err)
	}
	if now.Unix() >= claims.ExpiresUnix {
		return bad(errors.New("已过期"))
	}
	return claims, nil
}

// platformPayload 载体序列化，以 | 分隔；字段值不得含 |，由签发方校验。
func platformPayload(c PlatformClaims) string {
	return strings.Join([]string{
		platformPayloadVersion, c.TenantID, c.KeyID,
		strconv.FormatInt(c.ExpiresUnix, 10),
		strconv.FormatInt(c.RPMLimit, 10),
		strconv.FormatInt(c.TPMLimit, 10),
		strconv.FormatInt(c.DailyQuotaCredits, 10),
	}, "|")
}

func parsePlatformPayload(s string) (*PlatformClaims, error) {
	parts := strings.Split(s, "|")
	if len(parts) != 7 || parts[0] != platformPayloadVersion {
		return nil, errors.New("载体版本或字段数不符")
	}
	nums := make([]int64, 4)
	for i, raw := range parts[3:] {
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("数值字段非法: %w", err)
		}
		nums[i] = v
	}
	if strings.ContainsAny(parts[1]+parts[2], "|") {
		return nil, errors.New("标识含分隔符")
	}
	return &PlatformClaims{
		TenantID: parts[1], KeyID: parts[2], ExpiresUnix: nums[0],
		RPMLimit: nums[1], TPMLimit: nums[2], DailyQuotaCredits: nums[3],
	}, nil
}
