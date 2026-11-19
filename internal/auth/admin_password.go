package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// argon2 PHC 参数上限:拒绝资源越界串,不让读库触发巨额分配。
const (
	argon2MemoryMaxKiB = 256 * 1024
	argon2TimeMax      = 10
	argon2ParallelMax  = 16
	argon2SaltMinBytes = 8
	argon2SaltMaxBytes = 64
	argon2KeyLen       = 32 // 派生密钥宽度(与签发一致)
)

// HashAdminPassword 使用 OWASP 基线，盐与参数随 PHC 串存储。
func HashAdminPassword(password string) (string, error) {
	if !utf8.ValidString(password) {
		return "", fmt.Errorf("密码须为有效 UTF-8 文本")
	}
	if utf8.RuneCountInString(password) < 8 || len(password) > 4096 {
		return "", fmt.Errorf("密码须至少 8 个字符，且不超过 4096 字节")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, 2, 19*1024, 1, 32)
	return fmt.Sprintf("$argon2id$v=19$m=19456,t=2,p=1$%s$%s",
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyAdminPassword 参数随串迁移；损坏或资源越界的 PHC 拒绝，不让读库触发巨额分配。
func VerifyAdminPassword(hash, password string) bool {
	if len(password) > 4096 {
		return false
	}
	params, salt, expected, ok := parseArgon2PHC(hash)
	if !ok {
		return false
	}
	actual := argon2.IDKey([]byte(password), salt, params.iterations, params.memory,
		params.parallel, argon2KeyLen)
	return subtle.ConstantTimeCompare(actual, expected) == 1
}

// argon2Params PHC 参数段解析结果。
type argon2Params struct {
	memory     uint32
	iterations uint32
	parallel   uint8
}

// parseArgon2PHC 解构 "$argon2id$v=19$m=..,t=..,p=..$salt$key" 并做值域闸。
// 参数段须与规范化重排逐字一致,防同一串多写法绕过上限判定。
func parseArgon2PHC(hash string) (argon2Params, []byte, []byte, bool) {
	parts := strings.Split(hash, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" || parts[2] != "v=19" {
		return argon2Params{}, nil, nil, false
	}
	params, ok := parseArgon2Params(parts[3])
	if !ok {
		return argon2Params{}, nil, nil, false
	}
	salt, ok := decodeArgon2Field(parts[4], argon2SaltMinBytes, argon2SaltMaxBytes)
	if !ok {
		return argon2Params{}, nil, nil, false
	}
	// 派生长度钉死与签发同宽:长度本就由字节闸锁定,读取物化长度只会引入 int→uint32 转换。
	expected, ok := decodeArgon2Field(parts[5], argon2KeyLen, argon2KeyLen)
	if !ok {
		return argon2Params{}, nil, nil, false
	}
	return params, salt, expected, true
}

// decodeArgon2Field 解码 raw-base64 字段并做字节长度闸;超界即拒,供分配上限依据。
func decodeArgon2Field(enc string, minLen, maxLen int) ([]byte, bool) {
	raw, err := base64.RawStdEncoding.DecodeString(enc)
	if err != nil || len(raw) < minLen || len(raw) > maxLen {
		return nil, false
	}
	return raw, true
}

// parseArgon2Params 解析 "m=<KiB>,t=<次>,p=<并行度>" 并校验资源上限。
func parseArgon2Params(seg string) (argon2Params, bool) {
	var p argon2Params
	n, err := fmt.Sscanf(seg, "m=%d,t=%d,p=%d", &p.memory, &p.iterations, &p.parallel)
	if err != nil || n != 3 {
		return argon2Params{}, false
	}
	if p.memory < 8*uint32(p.parallel) || p.memory > argon2MemoryMaxKiB {
		return argon2Params{}, false
	}
	if p.iterations == 0 || p.iterations > argon2TimeMax || p.parallel == 0 || p.parallel > argon2ParallelMax {
		return argon2Params{}, false
	}
	if seg != fmt.Sprintf("m=%d,t=%d,p=%d", p.memory, p.iterations, p.parallel) {
		return argon2Params{}, false
	}
	return p, true
}
