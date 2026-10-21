// Package auth 负责 API key 和管理面身份的签发与校验。
// API key 明文只在签发时返回；存储中只保留哈希和用于列表辨认的前缀。
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
	"uuid"

	"github.com/solvela/muniu/internal/logging"
	"github.com/solvela/muniu/internal/protocol"
	"github.com/solvela/muniu/internal/store"
)

// base62Chars 签发字符表。
const base62Chars = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// plaintextEntropyBytes 随机熵字节数。
const plaintextEntropyBytes = 32

// plaintextBodyLen 32 字节熵的 base62 编码长度：ceil(256 / log2(62)) = 43。
const plaintextBodyLen = 43

// Generate 签发明文 key，形态为 mk- 加 32 字节 crypto/rand 的 base62 编码。
// 每次调用独立随机，全局唯一性由 256 位熵保证。
func Generate() string {
	raw := make([]byte, plaintextEntropyBytes)
	if _, err := rand.Read(raw); err != nil {
		// crypto/rand 失败属系统级灾难，直接 panic 是唯一正确动作
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

// Hash 明文转 sha256 hex，库中唯一形态，明文绝不落盘。
func Hash(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

// KeyRecord 签发与列表回执，供管理命令与控制台；明文只在 CreateKey 返回值出现一次。
// 限额与过期时刻如实透出，零值或空表示不限、永不过期。
// Status 是生命周期终态位(active|revoked)；Disabled 是可逆运行态启停，两者正交：
// 吊销优先于停用，恢复操作不得复活吊销 key。
type KeyRecord struct {
	ID                    string     `json:"id"`
	TenantID              string     `json:"tenant_id"`
	Name                  string     `json:"name"`
	Prefix                string     `json:"prefix"`
	RPMLimit              int64      `json:"rpm_limit"`
	TPMLimit              int64      `json:"tpm_limit"`
	DailyQuotaCredits     int64      `json:"daily_quota_credits"`
	ExpiresAt             *time.Time `json:"expires_at"`
	Status                string     `json:"status"`
	Disabled              bool       `json:"disabled"`
	MaxRequestCostCredits int64      `json:"max_request_cost_credits"`
	AllowedModels         []string   `json:"allowed_models"` // 模型别名白名单(nil/空 = 不限)
}

// KeyOpts 签发可选项，零值表示不限、永不过期。
type KeyOpts struct {
	TPMLimit              int64
	DailyQuotaCredits     int64
	ExpiresAt             *time.Time
	MaxRequestCostCredits int64    // 单请求成本上界；0 为不限，负值在入口拒绝。
	AllowedModels         []string // 模型别名白名单；nil 或空为不限，条目经归一化校验。
}

// normalizeAllowlist 白名单归一化，签发与 PATCH 共用：trim 非空条目、去重保首现序，
// 空白条目整单拒绝，返回 nil 表示不限。刻意不校验别名存在性：拓扑热替换下允许先于
// 别名签发，校验存在性会把白名单维护与拓扑顺序耦合。
func normalizeAllowlist(allowed []string) ([]string, error) {
	if len(allowed) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(allowed))
	seen := make(map[string]struct{}, len(allowed))
	for _, a := range allowed {
		a = strings.TrimSpace(a)
		if a == "" {
			return nil, store.MarkBusiness(errors.New("auth: allowed_models 含空白条目"))
		}
		if _, dup := seen[a]; dup {
			continue
		}
		seen[a] = struct{}{}
		out = append(out, a)
	}
	return out, nil
}

// CreateKey 签发并落库，返回回执与仅此一次可见的明文。
// 负值限额在入口拒绝，否则会被执行链解释为不限，与收紧意图相反。
func CreateKey(db *sql.DB, tenant, name string, rpm int64, opts KeyOpts) (string, *KeyRecord, error) {
	if tenant == "" {
		tenant = "default"
	}
	if rpm < 0 || opts.TPMLimit < 0 || opts.DailyQuotaCredits < 0 || opts.MaxRequestCostCredits < 0 {
		return "", nil, store.MarkBusiness(errors.New("auth: rpm/tpm/日配额/请求预算 不可为负"))
	}
	if rpm <= 0 {
		rpm = 60
	}
	allowed, err := normalizeAllowlist(opts.AllowedModels)
	if err != nil {
		return "", nil, err
	}
	// CLI 传租户名，FK 指向 tenants(id)，此处先解析，租户不存在即拒绝签发。
	// 仅真不存在才标记业务错；DB I/O 故障不得标记，否则会被 admin 出口当 400 外泄
	tenantID, err := store.TenantIDByName(db, tenant)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return "", nil, store.MarkBusiness(fmt.Errorf("auth: 租户 %q 不存在", tenant))
		}
		return "", nil, fmt.Errorf("auth: 解析租户: %w", err)
	}
	plaintext := Generate()
	id := uuid.NewV7().String()
	now := time.Now().UTC().Format(time.RFC3339)
	var expires any
	if opts.ExpiresAt != nil {
		expires = opts.ExpiresAt.UTC().Format(time.RFC3339)
	}
	if _, err := db.Exec(`INSERT INTO api_keys (id, tenant_id, name, key_hash, prefix, status,
		rpm_limit, tpm_limit, daily_quota_credits, expires_at, max_request_cost_credits, allowed_models, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, tenantID, name, Hash(plaintext), plaintext[:12], "active",
		rpm, opts.TPMLimit, opts.DailyQuotaCredits, expires, opts.MaxRequestCostCredits,
		store.MarshalAllowlist(allowed), now); err != nil {
		return "", nil, fmt.Errorf("auth: 落库 api_key: %w", err)
	}
	return plaintext, &KeyRecord{
		ID: id, TenantID: tenant, Name: name, Prefix: plaintext[:12], RPMLimit: rpm,
		TPMLimit: opts.TPMLimit, DailyQuotaCredits: opts.DailyQuotaCredits, ExpiresAt: opts.ExpiresAt,
		Status: "active", Disabled: false, MaxRequestCostCredits: opts.MaxRequestCostCredits,
		AllowedModels: allowed,
	}, nil
}

// LimitsPatch key 运行态限额的局部更新，PATCH 语义为省略字段不动。
// ClearExpires 显式清除过期；ExpiresAt 非 nil 且未 Clear 时设定新过期。
// Disabled 三态：true 停用并记 disabled_at、false 恢复并清列、nil 不动。
// AllowedModels 三态：nil 不动、ClearAllowModels 清除即恢复不限、非 nil 整体设定；
// 两者同现以清除优先，JSON 层三态互斥，此为内部 API 的防御性约定。
type LimitsPatch struct {
	RPMLimit              *int64
	TPMLimit              *int64
	DailyQuotaCredits     *int64
	ExpiresAt             *time.Time
	ClearExpires          bool
	Disabled              *bool
	MaxRequestCostCredits *int64
	AllowedModels         []string
	ClearAllowModels      bool
}

// UpdateKeyLimits 局部更新 key 限额、过期与启停；管理面 PATCH 入口，写后调用方须 Verifier.Invalidate。
// key id 不存在返回 store.ErrNotFound；负值限额拒绝并标记业务错，管理面出口 400。
func UpdateKeyLimits(db *sql.DB, keyID string, p LimitsPatch) (*KeyRecord, error) {
	if (p.RPMLimit != nil && *p.RPMLimit < 0) ||
		(p.TPMLimit != nil && *p.TPMLimit < 0) ||
		(p.DailyQuotaCredits != nil && *p.DailyQuotaCredits < 0) ||
		(p.MaxRequestCostCredits != nil && *p.MaxRequestCostCredits < 0) {
		return nil, store.MarkBusiness(errors.New("auth: rpm/tpm/日配额/请求预算 不可为负"))
	}
	sets, args := []string{}, []any{}
	add := func(col string, v any) { sets = append(sets, col+" = ?"); args = append(args, v) }
	if p.RPMLimit != nil {
		add("rpm_limit", *p.RPMLimit)
	}
	if p.TPMLimit != nil {
		add("tpm_limit", *p.TPMLimit)
	}
	if p.DailyQuotaCredits != nil {
		add("daily_quota_credits", *p.DailyQuotaCredits)
	}
	if p.MaxRequestCostCredits != nil {
		add("max_request_cost_credits", *p.MaxRequestCostCredits)
	}
	if p.ClearExpires {
		add("expires_at", nil)
	} else if p.ExpiresAt != nil {
		add("expires_at", p.ExpiresAt.UTC().Format(time.RFC3339))
	}
	if p.Disabled != nil {
		if *p.Disabled {
			add("disabled_at", time.Now().UTC().Format(time.RFC3339))
		} else {
			add("disabled_at", nil)
		}
	}
	var allowed []string
	if p.AllowedModels != nil {
		normalized, nerr := normalizeAllowlist(p.AllowedModels)
		if nerr != nil {
			return nil, nerr
		}
		allowed = normalized
	}
	if p.ClearAllowModels {
		add("allowed_models", nil)
	} else if p.AllowedModels != nil {
		add("allowed_models", store.MarshalAllowlist(allowed))
	}
	if len(sets) == 0 {
		// 无字段可更新时仍回读校验 key 存在性，幂等 no-op 也要保持 404 语义
		sets = append(sets, "rpm_limit = rpm_limit")
	}
	args = append(args, keyID)
	res, err := db.Exec(`UPDATE api_keys SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("auth: 更新 api_key 限额: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, store.ErrNotFound
	}
	return keyRecordByID(db, keyID)
}

// keyRecordByID 按 id 读回执，供 Update 后回传最新行；不存在返回 ErrNotFound。
func keyRecordByID(db *sql.DB, keyID string) (*KeyRecord, error) {
	var r KeyRecord
	var exp, dis, allow sql.NullString
	err := db.QueryRow(`SELECT id, tenant_id, name, prefix, rpm_limit, tpm_limit, daily_quota_credits,
		expires_at, status, disabled_at, max_request_cost_credits, allowed_models
		FROM api_keys WHERE id = ?`, keyID).
		Scan(&r.ID, &r.TenantID, &r.Name, &r.Prefix, &r.RPMLimit, &r.TPMLimit, &r.DailyQuotaCredits,
			&exp, &r.Status, &dis, &r.MaxRequestCostCredits, &allow)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("auth: 读 api_key: %w", err)
	}
	if exp.Valid && exp.String != "" {
		t, perr := time.Parse(time.RFC3339, exp.String)
		if perr != nil {
			return nil, fmt.Errorf("auth: api_key %s expires_at 非法: %w", r.Prefix, perr)
		}
		r.ExpiresAt = &t
	}
	// disabled_at 只需非空判定：形态合法性由 store 读侧严格解析并拒绝，宁拒不放行；
	// 同列两层口径不同是刻意的，auth 读侧不重复解析成本
	r.Disabled = dis.Valid && dis.String != ""
	allowed, perr := store.ParseAllowlist(allow)
	if perr != nil {
		return nil, fmt.Errorf("auth: api_key %s: %w", r.Prefix, perr)
	}
	r.AllowedModels = allowed
	return &r, nil
}

// RevokeKeyByPrefix 按 prefix 吊销，管理命令入口；返回吊销行数。
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

// ListKeys 列出 key，管理命令读侧，不含哈希明文，按签发时间升序。
func ListKeys(db *sql.DB) ([]KeyRecord, error) {
	rows, err := db.Query(`SELECT id, tenant_id, name, prefix, rpm_limit, tpm_limit, daily_quota_credits,
		expires_at, status, disabled_at, max_request_cost_credits, allowed_models
		FROM api_keys ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("auth: 列出 api_key: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []KeyRecord
	for rows.Next() {
		var r KeyRecord
		var exp, dis, allow sql.NullString
		if err := rows.Scan(&r.ID, &r.TenantID, &r.Name, &r.Prefix, &r.RPMLimit, &r.TPMLimit,
			&r.DailyQuotaCredits, &exp, &r.Status, &dis, &r.MaxRequestCostCredits, &allow); err != nil {
			return nil, fmt.Errorf("auth: 扫 api_key 行: %w", err)
		}
		if exp.Valid && exp.String != "" {
			t, perr := time.Parse(time.RFC3339, exp.String)
			if perr != nil {
				return nil, fmt.Errorf("auth: api_key %s expires_at 非法: %w", r.Prefix, perr)
			}
			r.ExpiresAt = &t
		}
		r.Disabled = dis.Valid && dis.String != ""
		allowed, perr := store.ParseAllowlist(allow)
		if perr != nil {
			return nil, fmt.Errorf("auth: api_key %s: %w", r.Prefix, perr)
		}
		r.AllowedModels = allowed
		out = append(out, r)
	}
	return out, rows.Err()
}

// ---- 校验中间件 ----

// Options 校验缓存参数，零值取默认：TTL 10s、容量 10k。
// 平台 key 三项可选：Secrets 空即未启用，mk2- 一律拒绝；RevocationsURL 空则不做吊销同步，
// 平台 key 仅受有效期约束，高危场景应配置。
type Options struct {
	TTL      time.Duration
	Capacity int

	PlatformSecrets     [][]byte      // HMAC 验签密钥，任一通过即有效，支持轮换。
	RevocationsURL      string        // 吊销名单端点，格式 {"version":N,"revoked":["key_id",...]}。
	RevocationsInterval time.Duration // 拉取间隔即吊销收敛窗口，零值取 60s。
}

// Verifier Bearer key 校验器，中间件持有，进程内单例。
type Verifier struct {
	db     *sql.DB
	opts   Options
	logger *slog.Logger
	now    func() time.Time // 时钟源，供过期判定与测试注入

	mu    sync.Mutex
	cache map[string]cacheEntry // key_hash → 条目

	revMu       sync.RWMutex
	revoked     map[string]struct{}
	revVersion  int64
	revStop     chan struct{}
	revStopOnce sync.Once
	revClient   *http.Client
}

// errKeyExpired 过期哨兵，Middleware 据此给出已过期 message，与无效、吊销可区分。
var errKeyExpired = errors.New("key 已过期")

// errKeyDisabled 停用哨兵，Middleware 据此给出已停用 message，用户动作为找管理员重新启用。
var errKeyDisabled = errors.New("key 已停用")

// cacheEntry 缓存条目，任意字段访问须持 v.mu。
type cacheEntry struct {
	info      KeyInfo
	expiresAt time.Time
}

// KeyInfo 鉴权成功后挂到 ctx 的 key 信息，不含任何明文与哈希。
// ExpiresAt 为查库 key 的过期时刻，零值表示永不过期或已由平台验签判定。
// 过期判定收口在 verify 成功后的单点，以当前时钟逐请求执行，缓存命中路径同样精确；
// 缓存只影响 DB 变更可见性，不影响时间函数。
type KeyInfo struct {
	ID                    string
	TenantID              string
	Name                  string
	RPMLimit              int64
	TPMLimit              int64
	DailyQuotaCredits     int64
	Currency              string
	ExpiresAt             time.Time
	MaxRequestCostCredits int64    // 单请求成本上界；0 为不限，由请求级成本预算闸消费。
	AllowedModels         []string // 模型别名白名单；nil 或空为不限，由数据面执行链消费。
}

type ctxKey struct{}

// NewVerifier 构造校验器；配置了吊销端点时启动拉取循环，Close 退出。
func NewVerifier(db *sql.DB, opts Options, logger *slog.Logger) *Verifier {
	if opts.TTL <= 0 {
		opts.TTL = 10 * time.Second
	}
	if opts.Capacity <= 0 {
		opts.Capacity = 10_000
	}
	if logger == nil {
		logger = slog.Default()
	}
	// 缺省修正必须在构造副本前完成，零值 interval 会使 NewTicker panic
	if opts.RevocationsInterval <= 0 {
		opts.RevocationsInterval = 60 * time.Second
	}
	v := &Verifier{
		db: db, opts: opts, logger: logger, now: time.Now,
		cache:     map[string]cacheEntry{},
		revClient: &http.Client{Timeout: 5 * time.Second},
	}
	if opts.RevocationsURL != "" {
		v.revoked = map[string]struct{}{}
		v.revStop = make(chan struct{})
		go v.revocationLoop() // 启动即拉一次，不等首个周期，重启后吊销立即收敛
	}
	return v
}

// Close 停止吊销同步循环，供进程退出与测试收尾，并发安全。
func (v *Verifier) Close() {
	if v.revStop == nil {
		return
	}
	v.revStopOnce.Do(func() { close(v.revStop) })
}

// revocationLoop 吊销名单拉取循环：启动即拉，失败保持上一次快照并告警；
// 版本单调，仅应用 version >= 本地版本的快照以防旧快照回退；Close 经 ctx 立即中断在途请求。
func (v *Verifier) revocationLoop() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-v.revStop
		cancel()
	}()
	fetch := func() {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.opts.RevocationsURL, nil)
		if err != nil {
			v.logger.Warn("吊销名单拉取失败", "error_detail", err.Error())
			return
		}
		resp, err := v.revClient.Do(req)
		if err != nil {
			select {
			case <-v.revStop:
				return
			default:
			}
			v.logger.Warn("吊销名单拉取失败", "error_detail", err.Error())
			return
		}
		var body struct {
			Version int64    `json:"version"`
			Revoked []string `json:"revoked"`
		}
		decodeErr := json.NewDecoder(resp.Body).Decode(&body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			v.logger.Warn("吊销名单拉取失败", "status", resp.StatusCode)
			return
		}
		if decodeErr != nil {
			v.logger.Warn("吊销名单响应非法", "error_detail", decodeErr.Error())
			return
		}
		set := make(map[string]struct{}, len(body.Revoked))
		for _, id := range body.Revoked {
			set[id] = struct{}{}
		}
		v.revMu.Lock()
		if body.Version >= v.revVersion { // 单调防回退：乱序或滞后的旧快照不得洗白已吊销 key
			v.revoked = set
			v.revVersion = body.Version
		}
		v.revMu.Unlock()
	}
	fetch()
	ticker := time.NewTicker(v.opts.RevocationsInterval)
	defer ticker.Stop()
	for {
		select {
		case <-v.revStop:
			return
		case <-ticker.C:
			fetch()
		}
	}
}

// revokedContains 平台 key 吊销判定；无快照表示未启用同步，视作不吊销。
func (v *Verifier) revokedContains(keyID string) bool {
	v.revMu.RLock()
	defer v.revMu.RUnlock()
	_, ok := v.revoked[keyID]
	return ok
}

// Middleware 返回鉴权中间件：成功将 KeyInfo 挂 ctx，失败返回 401 auth_failed 信封。
// 过期与无效、吊销同归 auth_failed，但 message 可区分，因用户动作不同：
// 过期是重签，吊销是找管理员。
func (v *Verifier) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		info, err := v.verify(r)
		if err != nil {
			// 技术细节进日志，对外只给归一化 message
			v.logger.Warn("鉴权失败", "request_id", logging.RequestIDFrom(r.Context()), "error_detail", err.Error())
			msg := "API key 无效或已吊销"
			if errors.Is(err, errKeyExpired) {
				msg = "API key 已过期,请重新签发"
			} else if errors.Is(err, errKeyDisabled) {
				msg = "API key 已停用,请联系管理员重新启用"
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(protocol.StatusForType(protocol.TypeAuthFailed))
			_ = json.NewEncoder(w).Encode(protocol.NewErrorEnvelope(
				protocol.TypeAuthFailed, "auth_failed", msg, logging.RequestIDFrom(r.Context())))
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, info)))
	})
}

// verify 单次校验：平台 key(mk2-)走离线验真，查库 key(mk-)走缓存或查库；
// 成功后以当前时钟做单点过期判定，查库与缓存命中两路径同样精确。
func (v *Verifier) verify(r *http.Request) (*KeyInfo, error) {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if h == "" {
		return nil, errors.New("缺少 Authorization 头")
	}
	if !strings.HasPrefix(h, prefix) {
		return nil, errors.New("鉴权头非 Bearer 形态")
	}
	raw := strings.TrimSpace(h[len(prefix):])
	var info *KeyInfo
	var err error
	if strings.HasPrefix(raw, PlatformKeyPrefix) {
		info, err = v.verifyPlatform(raw)
	} else {
		info, err = v.verifyLookup(raw)
	}
	if err != nil {
		return nil, err
	}
	if !info.ExpiresAt.IsZero() && v.now().After(info.ExpiresAt) {
		return nil, errKeyExpired
	}
	return info, nil
}

// verifyPlatform 自校验平台 key：HMAC 离线验真加吊销快照；结果不进缓存，HMAC 为 µs 级，缓存只会拖延吊销生效。
func (v *Verifier) verifyPlatform(raw string) (*KeyInfo, error) {
	if len(v.opts.PlatformSecrets) == 0 {
		return nil, errors.New("平台 key 未启用")
	}
	claims, err := ParsePlatformKey(raw, v.opts.PlatformSecrets, time.Now())
	if err != nil {
		return nil, err
	}
	if v.revokedContains(claims.KeyID) {
		return nil, errors.New("平台 key 已吊销")
	}
	return &KeyInfo{
		ID:                claims.KeyID,
		TenantID:          claims.TenantID,
		RPMLimit:          claims.RPMLimit,
		TPMLimit:          claims.TPMLimit,
		DailyQuotaCredits: claims.DailyQuotaCredits,
	}, nil
}

func (v *Verifier) verifyLookup(raw string) (*KeyInfo, error) {
	plaintext, err := bearerOfValue(raw)
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
	// 可逆停用：吊销优先已判，此处补停用判定；哨兵供 Middleware 区分文案
	if row.DisabledAt != nil {
		return nil, errKeyDisabled
	}
	// 租户级冻结：管理面 disable 租户必须对数据面即时生效，租户状态随本查询 JOIN 返回
	if row.TenantStatus != "" && row.TenantStatus != "active" {
		return nil, errors.New("租户已禁用")
	}
	info := &KeyInfo{
		ID:                    row.ID,
		TenantID:              row.TenantID,
		Name:                  row.Name,
		RPMLimit:              row.RPMLimit,
		TPMLimit:              row.TPMLimit,
		DailyQuotaCredits:     row.DailyQuotaCredits,
		Currency:              row.Currency,
		MaxRequestCostCredits: row.MaxRequestCostCredits,
		AllowedModels:         row.AllowedModels,
	}
	if row.ExpiresAt != nil {
		info.ExpiresAt = *row.ExpiresAt
	}

	v.mu.Lock()
	v.cacheInsertLocked(hash, info, now)
	v.mu.Unlock()
	return info, nil
}

// cacheInsertLocked 插入缓存，满容量时随机淘汰一条以防 hash 探测侧信道，无需 LRU 精确性。
// 调用方必须持 v.mu。
func (v *Verifier) cacheInsertLocked(hash string, info *KeyInfo, now time.Time) {
	if len(v.cache) >= v.opts.Capacity {
		for victim := range v.cache { // map 遍历序随机，取其一淘汰
			delete(v.cache, victim)
			break
		}
	}
	v.cache[hash] = cacheEntry{info: *info, expiresAt: now.Add(v.opts.TTL)}
}

// Invalidate 清空缓存，使吊销与停用立即生效；管理路径调用。
func (v *Verifier) Invalidate() {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.cache = map[string]cacheEntry{}
}

// bearerOfValue key 明文的形态校验：mk- 前缀加 46 字符总长。
func bearerOfValue(key string) (string, error) {
	if len(key) != 3+plaintextBodyLen || !strings.HasPrefix(key, "mk-") {
		return "", errors.New("key 形态非法")
	}
	return key, nil
}

// KeyFrom 从 ctx 取鉴权信息，仅鉴权中间件之后的 handler 可得，缺失返回 nil。
func KeyFrom(ctx context.Context) *KeyInfo {
	if v, ok := ctx.Value(ctxKey{}).(*KeyInfo); ok {
		return v
	}
	return nil
}

// WithKeyInfo 将鉴权后的 key 信息注入请求上下文，供限流共享后端的中间件测试使用；
// 生产路径由鉴权中间件注入，不经过本函数。
func WithKeyInfo(ctx context.Context, info *KeyInfo) context.Context {
	return context.WithValue(ctx, ctxKey{}, info)
}
