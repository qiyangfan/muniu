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
	return fmt.Sprintf("$argon2id$v=19$m=19456,t=2,p=1$%s$%s", base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyAdminPassword 参数随串迁移；损坏或资源越界的 PHC 拒绝，不让读库触发巨额分配。
func VerifyAdminPassword(hash, password string) bool {
	if len(password) > 4096 {
		return false
	}
	parts := strings.Split(hash, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" || parts[2] != "v=19" {
		return false
	}
	var memory, iterations uint32
	var parallel uint8
	n, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &parallel)
	if err != nil || n != 3 || memory < 8*uint32(parallel) || memory > 256*1024 || iterations == 0 || iterations > 10 || parallel == 0 || parallel > 16 {
		return false
	}
	if parts[3] != fmt.Sprintf("m=%d,t=%d,p=%d", memory, iterations, parallel) {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) < 8 || len(salt) > 64 {
		return false
	}
	expected, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(expected) < 16 || len(expected) > 64 {
		return false
	}
	actual := argon2.IDKey([]byte(password), salt, iterations, memory, parallel, uint32(len(expected)))
	return subtle.ConstantTimeCompare(actual, expected) == 1
}
