// Package control 承载数据面与控制面的消费侧契约：服务身份、注册心跳、统一快照、
// 用量/健康上报与运行时命令。所有控制操作失败都不阻塞数据面。
package control

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"time"
)

// SchemaVersion 控制面契约的本地消费版本；X-01 冻结后随契约升级。
const SchemaVersion = 1

// ServiceID 本服务在锦官注册表中的稳定标识。
const ServiceID = "muniu"

// Capabilities 能力键单一来源；与锦官 service-registry 契约同名。
func Capabilities() []string {
	return []string{
		"llm.chat", "llm.responses", "llm.messages", "llm.gemini",
		"llm.images", "llm.embeddings", "llm.multimodal_embeddings",
		"cache.semantic", "usage.llm", "health",
	}
}

// EntitlementKeys 本服务执行/上报的权益键；LLM token/费用只由 muniu 产生。
func EntitlementKeys() []string {
	return []string{"llm.tokens", "llm.requests", "llm.images", "cache.semantic"}
}

// AdminAPIs 服务 admin API 能力，供控制面投影与诊断。
func AdminAPIs() []string {
	return []string{
		"api_keys", "providers", "plugins", "oauth_accounts", "balance",
		"semantic_cache", "usage", "audit", "runtime_commands",
	}
}

// SnapshotEnvelope 控制快照信封：版本/观测时间 + 按服务投影的负载。
type SnapshotEnvelope struct {
	Version    int64           `json:"version"`
	ObservedAt time.Time       `json:"observed_at"`
	Payload    SnapshotPayload `json:"payload"`
}

// SnapshotPayload 共享控制投影。安全类（吊销/租户）缺失即整包拒绝（fail-closed）。
type SnapshotPayload struct {
	Pricing     []PricingEntry `json:"pricing"`
	Topology    *Topology      `json:"topology"`
	Revocations *Revocations   `json:"revocations"`
	Tenants     []Tenant       `json:"tenants"`
	Policies    any            `json:"policies,omitempty"`
}

// PricingEntry 价目快照条目；金额为 credit 十进制字符串（与 muniu ParsePrice 同口径）。
type PricingEntry struct {
	Alias       string          `json:"alias"`
	Currency    string          `json:"currency"`
	Input       string          `json:"input"`
	CachedInput string          `json:"cached_input"`
	VisionInput string          `json:"vision_input"`
	Output      string          `json:"output"`
	Timezone    string          `json:"timezone"`
	TimeWindows []PricingWindow `json:"time_windows"`
	UpdatedAt   string          `json:"updated_at"`
}

// PricingWindow 分时段倍率窗。
type PricingWindow struct {
	Window     string `json:"window"`
	Multiplier string `json:"multiplier"`
}

// Topology 模型拓扑投影（providers/model_routes/models 三件套）。
type Topology struct {
	Providers   []Provider         `json:"providers"`
	ModelRoutes map[string][]Route `json:"model_routes"`
	Models      []ModelCapability  `json:"models"`
}

// Provider 供应商定义；keys 只含 env/account 引用，不含明文。
type Provider struct {
	Name                          string   `json:"name"`
	Adapter                       string   `json:"adapter"`
	BaseURL                       string   `json:"base_url"`
	Auth                          string   `json:"auth,omitempty"`
	NativeMultimodalEmbeddingsURL string   `json:"native_multimodal_embeddings_url,omitempty"`
	Keys                          []KeyRef `json:"keys"`
	Weight                        int      `json:"weight"`
	Priority                      int      `json:"priority"`
}

// KeyRef key 引用：env 名或 oauth 账号 id。
type KeyRef struct {
	Env     string `json:"env"`
	Account string `json:"account"`
	Weight  int    `json:"weight"`
}

// Route 模型路由候选。
type Route struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Priority int    `json:"priority"`
	Weight   int    `json:"weight"`
}

// ModelCapability 模型能力声明。
type ModelCapability struct {
	Provider        string   `json:"provider"`
	Model           string   `json:"model"`
	DropParams      []string `json:"drop_params"`
	ThinkingLock    bool     `json:"thinking_lock"`
	MaxOutputTokens int64    `json:"max_output_tokens"`
}

// Revocations 平台 key 全量吊销名单；版本单调。
type Revocations struct {
	Version int64    `json:"version"`
	Revoked []string `json:"revoked"`
}

// Tenant 租户目录与配额投影；0=不限。
type Tenant struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	Status            string `json:"status"`
	RPMLimit          int64  `json:"rpm_limit"`
	TPMLimit          int64  `json:"tpm_limit"`
	DailyQuotaCredits int64  `json:"daily_quota_credits"`
}

// ValidatePayload 安全类结构校验：吊销/租户必须在场且合法，否则整包拒绝保留 last-known。
func ValidatePayload(p SnapshotPayload) error {
	if p.Revocations == nil {
		return errors.New("控制快照缺少吊销名单(安全类)")
	}
	if p.Revocations.Version < 0 {
		return errors.New("吊销名单版本不可为负")
	}
	if p.Tenants == nil {
		return errors.New("控制快照缺少租户目录(安全类)")
	}
	if err := validateTenants(p.Tenants); err != nil {
		return err
	}
	return validatePricing(p.Pricing)
}

// validateTenants 租户目录：id/name 唯一、状态与限额合法。
func validateTenants(tenants []Tenant) error {
	seenID := map[string]bool{}
	seenName := map[string]bool{}
	for i, t := range tenants {
		if err := validateTenant(i, t); err != nil {
			return err
		}
		if seenID[t.ID] || seenName[t.Name] {
			return fmt.Errorf("租户 id/name 重复: %s/%s", t.ID, t.Name)
		}
		seenID[t.ID] = true
		seenName[t.Name] = true
	}
	return nil
}

// validateTenant 单租户字段校验。
func validateTenant(i int, t Tenant) error {
	if t.ID == "" || t.Name == "" {
		return fmt.Errorf("租户[%d]缺少 id/name", i)
	}
	if t.Status != "active" && t.Status != "disabled" {
		return fmt.Errorf("租户 %s status 非法 %q", t.ID, t.Status)
	}
	if t.RPMLimit < 0 || t.TPMLimit < 0 || t.DailyQuotaCredits < 0 {
		return fmt.Errorf("租户 %s 限额不可为负", t.ID)
	}
	return nil
}

// validatePricing 价目别名必填且唯一（金额解析由装配层逐条校验）。
func validatePricing(entries []PricingEntry) error {
	seenAlias := map[string]bool{}
	for i, pe := range entries {
		if pe.Alias == "" {
			return fmt.Errorf("价目[%d]缺少 alias", i)
		}
		if seenAlias[pe.Alias] {
			return fmt.Errorf("价目别名重复: %s", pe.Alias)
		}
		seenAlias[pe.Alias] = true
	}
	return nil
}

// LoadPublicKey 读取 RSA 公钥 PEM（PKIX 或 PKCS#1）。
func LoadPublicKey(path string) (*rsa.PublicKey, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // 路径来自运维配置的验签公钥文件
	if err != nil {
		return nil, fmt.Errorf("control: 读取快照公钥: %w", err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, errors.New("control: 快照公钥非 PEM")
	}
	if key, err := x509.ParsePKIXPublicKey(block.Bytes); err == nil {
		if pub, ok := key.(*rsa.PublicKey); ok {
			return pub, nil
		}
		return nil, errors.New("control: 快照公钥非 RSA")
	}
	pub, err := x509.ParsePKCS1PublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("control: 解析快照公钥: %w", err)
	}
	return pub, nil
}
