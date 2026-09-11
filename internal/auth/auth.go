// Package auth 承担 apikey 签发与校验(T2.2):
// 明文形如 mk-<43 位 base62>,仅签发时展示一次;库中只存 sha256 hex(key_hash)与
// 明文前 12 字符(prefix,供列表辨认)。校验走进程内缓存(map+mutex,TTL 过期 + 容量随机
// 淘汰,不引 LRU 库——conventions 依赖纪律),未命中查库。
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/solvela/muniu/internal/logging"
	"github.com/solvela/muniu/internal/protocol"
	"github.com/solvela/muniu/internal/store"
)

// base62Chars 签发字符表(base62,字典序无特殊含义)。
const base62Chars = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// plaintextEntropyBytes 随机熵字节数(256 位 → base62 43 字符)。
const plaintextEntropyBytes = 32

// plaintextBodyLen 32 字节熵的 base62 编码长度:ceil(256 / log2(62)) = 43。
const plaintextBodyLen = 43

// Generate 签发明文 key:mk- + 32 字节 crypto/rand 的 base62 编码。
// 每次调用独立随机,全局唯一性由 256 位熵保证。
func Generate() string {
	raw := make([]byte, plaintextEntropyBytes)
	if _, err := rand.Read(raw); err != nil {
		// crypto/rand 失败属于系统级灾难,直接 panic 是唯一正确动作
		panic(fmt.Sprintf("auth: crypto/rand 不可用: %v", err))
	}
	n := big.NewInt(int64(len(base62Chars)))
	x := new(big.Int).SetBytes(raw)
	mod := new(big.Int)
	out := make([]byte, plaintextBodyLen)
	for i := plaintextBodyLen - 1; i >= 0; i-- {
		x.DivMod(x, n, mod)
		out[i] = base62Chars[mod.Int64()]
	}
	return "mk-" + string(out)
}

// Hash 明文 → sha256 hex(库中唯一形态,明文绝不落盘)。
func Hash(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

// KeyRecord 签发结果回执(管理命令打印;明文只在 CreateKey 返回值出现一次)。
type KeyRecord struct {
	ID       string `json:"id"`
	TenantID string `json:"tenant_id"`
	Name     string `json:"name"`
	Prefix   string `json:"prefix"`
	RPMLimit int64  `json:"rpm_limit"`
}

// CreateKey 签发并落库;返回明文(仅此一次)与回执。
func CreateKey(db *sql.DB, tenant, name string, rpm int64) (string, *KeyRecord, error) {
	if tenant == "" {
		tenant = "default"
	}
	if rpm <= 0 {
		rpm = 60
	}
	// CLI 传租户名;FK 指向 tenants(id),此处先解析,租户不存在即拒绝签发。
	// 只有「真不存在」才是业务错;DB I/O 故障不得标记(会被 admin 出口当 400 外泄驱动错误)
	tenantID, err := store.TenantIDByName(db, tenant)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return "", nil, store.MarkBusiness(fmt.Errorf("auth: 租户 %q 不存在", tenant))
		}
		return "", nil, fmt.Errorf("auth: 解析租户: %w", err)
	}
	plaintext := Generate()
	id := uuid.Must(uuid.NewV7()).String()
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.Exec(`INSERT INTO api_keys (id, tenant_id, name, key_hash, prefix, status, rpm_limit, created_at)
		VALUES (?,?,?,?,?,?,?,?)`,
		id, tenantID, name, Hash(plaintext), plaintext[:12], "active", rpm, now); err != nil {
		return "", nil, fmt.Errorf("auth: 落库 api_key: %w", err)
	}
	return plaintext, &KeyRecord{
		ID: id, TenantID: tenant, Name: name, Prefix: plaintext[:12], RPMLimit: rpm,
	}, nil
}

// RevokeKeyByPrefix 按 prefix 吊销(管理命令入口);返回吊销行数。
func RevokeKeyByPrefix(db *sql.DB, prefix string) (int64, error) {
	res, err := db.Exec(`UPDATE api_keys
		SET status = 'revoked', revoked_at = ?
		WHERE prefix = ? AND status = 'active'`,
		time.Now().UTC().Format(time.RFC3339), prefix)
	if err != nil {
		return 0, fmt.Errorf("auth: 吊销 api_key: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// ListKeys 列出 key(管理命令读侧;不含 hash 明文)。
func ListKeys(db *sql.DB) ([]KeyRecord, error) {
	rows, err := db.Query(`SELECT id, tenant_id, name, prefix, rpm_limit FROM api_keys ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("auth: 列出 api_key: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []KeyRecord
	for rows.Next() {
		var r KeyRecord
		if err := rows.Scan(&r.ID, &r.TenantID, &r.Name, &r.Prefix, &r.RPMLimit); err != nil {
			return nil, fmt.Errorf("auth: 扫 api_key 行: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ---- 校验中间件 ----

// Options 校验缓存参数;零值取默认(TTL 10s,容量 10k)。
type Options struct {
	TTL      time.Duration
	Capacity int
}

// Verifier Bearer key 校验器(中间件持有,进程内单例)。
type Verifier struct {
	db     *sql.DB
	opts   Options
	logger *slog.Logger

	mu    sync.Mutex
	cache map[string]cacheEntry // key_hash → 条目
}

// cacheEntry 缓存条目:任意字段访问须持 v.mu。
type cacheEntry struct {
	info      KeyInfo
	expiresAt time.Time
}

// KeyInfo 鉴权成功后挂到 ctx 的 key 信息(不含任何明文/哈希)。
type KeyInfo struct {
	ID              string
	TenantID        string
	Name            string
	RPMLimit        int64
	TPMLimit        int64
	DailyQuotaMicro int64
	Currency        string
}

type ctxKey struct{}

// NewVerifier 构造校验器。
func NewVerifier(db *sql.DB, opts Options, logger *slog.Logger) *Verifier {
	if opts.TTL <= 0 {
		opts.TTL = 10 * time.Second
	}
	if opts.Capacity <= 0 {
		opts.Capacity = 10_000
	}
	return &Verifier{db: db, opts: opts, logger: logger, cache: map[string]cacheEntry{}}
}

// Middleware 返回鉴权中间件:成功 → KeyInfo 挂 ctx;失败 → 401 auth_failed 信封。
func (v *Verifier) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		info, err := v.verify(r)
		if err != nil {
			// 技术细节进日志,对外只给归一化 message(conventions「日志」)
			v.logger.Warn("鉴权失败", "request_id", logging.RequestIDFrom(r.Context()), "error_detail", err.Error())
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(protocol.StatusForType(protocol.TypeAuthFailed))
			_ = json.NewEncoder(w).Encode(protocol.NewErrorEnvelope(
				protocol.TypeAuthFailed, "auth_failed", "API key 无效或已吊销", logging.RequestIDFrom(r.Context())))
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, info)))
	})
}

// verify 单次校验:缓存命中且未过期直接放行,否则查库并回填。
func (v *Verifier) verify(r *http.Request) (*KeyInfo, error) {
	plaintext, err := bearerOf(r)
	if err != nil {
		return nil, err
	}
	hash := Hash(plaintext)
	now := time.Now()

	v.mu.Lock()
	if e, ok := v.cache[hash]; ok && now.Before(e.expiresAt) {
		v.mu.Unlock()
		return &e.info, nil
	}
	v.mu.Unlock()

	row, err := store.GetAPIKeyByHash(v.db, hash)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, errors.New("key 不存在")
		}
		return nil, fmt.Errorf("查库失败: %w", err)
	}
	if row.Status != "active" {
		return nil, errors.New("key 已吊销或禁用")
	}
	// 租户级冻结:管理面 disable 租户必须对数据面即时生效(随本查询 JOIN 返回)
	if row.TenantStatus != "" && row.TenantStatus != "active" {
		return nil, errors.New("租户已禁用")
	}
	info := &KeyInfo{
		ID:              row.ID,
		TenantID:        row.TenantID,
		Name:            row.Name,
		RPMLimit:        row.RPMLimit,
		TPMLimit:        row.TPMLimit,
		DailyQuotaMicro: row.DailyQuotaMicro,
		Currency:        row.Currency,
	}

	v.mu.Lock()
	v.cacheInsertLocked(hash, info, now)
	v.mu.Unlock()
	return info, nil
}

// cacheInsertLocked 插入缓存;满容量时随机淘汰一条(防 hash 探测侧信道,无需 LRU 精确性)。
// 调用方必须持 v.mu。
func (v *Verifier) cacheInsertLocked(hash string, info *KeyInfo, now time.Time) {
	if len(v.cache) >= v.opts.Capacity {
		for victim := range v.cache { // map 遍历序随机,取其一淘汰
			delete(v.cache, victim)
			break
		}
	}
	v.cache[hash] = cacheEntry{info: *info, expiresAt: now.Add(v.opts.TTL)}
}

// Invalidate 清空缓存(吊销/禁用后立即生效;管理路径调用)。
func (v *Verifier) Invalidate() {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.cache = map[string]cacheEntry{}
}

// bearerOf 解析 Authorization: Bearer <key>;任何形态问题都归一为校验失败。
func bearerOf(r *http.Request) (string, error) {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if h == "" {
		return "", errors.New("缺少 Authorization 头")
	}
	if !strings.HasPrefix(h, prefix) {
		return "", errors.New("鉴权头非 Bearer 形态")
	}
	key := strings.TrimSpace(h[len(prefix):])
	if len(key) != 3+plaintextBodyLen || !strings.HasPrefix(key, "mk-") {
		return "", errors.New("key 形态非法")
	}
	return key, nil
}

// KeyFrom 从 ctx 取鉴权信息(仅鉴权中间件之后的 handler 可得;缺失返回 nil)。
func KeyFrom(ctx context.Context) *KeyInfo {
	if v, ok := ctx.Value(ctxKey{}).(*KeyInfo); ok {
		return v
	}
	return nil
}
