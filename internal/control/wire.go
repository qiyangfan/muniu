// 锦官 X-01 快照 wire 形态：信封 + 分节投影。内部仍以 SnapshotEnvelope 作为
// 应用层输入，本文件只做 wire → 领域模型的无损映射。
package control

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// 安全节清单固定；缺任一节即整包拒绝。
var muniuSecuritySections = []string{
	"tenant_directory", "entitlements", "policy_packages", "key_revocations",
}

// WireSnapshot 锦官 GET /control/v1/snapshot 响应体（签名在响应头）。
type WireSnapshot struct {
	Version          int64        `json:"version"`
	ObservedAt       time.Time    `json:"observed_at"`
	ServiceID        string       `json:"service_id"`
	ETag             string       `json:"etag"`
	SecuritySections []string     `json:"security_sections"`
	Sections         WireSections `json:"sections"`
}

// WireSections 按服务投影的分节载荷；muniu 只消费下列节。
type WireSections struct {
	TenantDirectory *WireTenantDirectory `json:"tenant_directory"`
	Entitlements    *WireEntitlements    `json:"entitlements"`
	PolicyPackages  json.RawMessage      `json:"policy_packages"`
	Pricing         *WirePricing         `json:"pricing"`
	ImagePricing    *WireImagePricing    `json:"image_pricing"`
	Topology        *Topology            `json:"topology"`
	KeyRevocations  *Revocations         `json:"key_revocations"`
}

// WireTenantDirectory 租户目录（含成员，但 muniu 只取租户字段）。
type WireTenantDirectory struct {
	Tenants []WireTenant `json:"tenants"`
}

// WireTenant 锦官租户视图；tenantKey 用于把权益配额对回租户。
type WireTenant struct {
	ID        string `json:"id"`
	TenantKey string `json:"tenantKey"`
	Name      string `json:"name"`
	Status    string `json:"status"`
}

// WireEntitlements 租户权益投影；配额维度由服务自行解释。
type WireEntitlements struct {
	Tenants []WireEntitlement `json:"tenants"`
}

// WireEntitlement 单个租户的生效套餐与配额。
type WireEntitlement struct {
	TenantKey       string      `json:"tenantKey"`
	Quotas          []WireQuota `json:"quotas"`
	EntitlementKeys []string    `json:"entitlementKeys"`
}

// WireQuota 配额维度；muniu 解释 rpm/tpm/daily_quota_credits/max_inflight。
type WireQuota struct {
	Dimension string `json:"dimension"`
	Unit      string `json:"unit"`
	Limit     int64  `json:"limit"`
}

// WirePricing 价目节；锦官外层固定带 pricing 数组。
type WirePricing struct {
	Pricing []PricingEntry `json:"pricing"`
}

// WireImagePricing 图片价目节；缺席保留，空数组清空。
type WireImagePricing struct {
	Entries []ImagePricingEntry `json:"entries"`
}

// ToEnvelope 校验服务标识/安全节并把 wire 投影映射为应用层快照。
func (w *WireSnapshot) ToEnvelope(expectedServiceID string) (*SnapshotEnvelope, error) {
	if err := w.validate(expectedServiceID); err != nil {
		return nil, err
	}
	tenants, err := w.tenants()
	if err != nil {
		return nil, err
	}
	payload := w.payload(tenants)
	if err := ValidatePayload(payload); err != nil {
		return nil, err
	}
	return &SnapshotEnvelope{
		Version: w.Version, ObservedAt: w.ObservedAt, Payload: payload,
	}, nil
}

// validate 检查服务标识、版本与四类安全节。
func (w *WireSnapshot) validate(expectedServiceID string) error {
	if w.ServiceID != expectedServiceID {
		return fmt.Errorf("control: 快照 service_id 不符 %q", w.ServiceID)
	}
	if w.Version <= 0 {
		return errors.New("control: 快照版本必须为正")
	}
	if w.ObservedAt.IsZero() {
		return errors.New("control: 快照缺少 observed_at")
	}
	if err := requireSecuritySections(w.SecuritySections); err != nil {
		return err
	}
	if w.Sections.TenantDirectory == nil {
		return errors.New("control: 快照缺 tenant_directory(安全类)")
	}
	if w.Sections.Entitlements == nil {
		return errors.New("control: 快照缺 entitlements(安全类)")
	}
	if len(w.Sections.PolicyPackages) == 0 {
		return errors.New("control: 快照缺 policy_packages(安全类)")
	}
	if w.Sections.KeyRevocations == nil {
		return errors.New("control: 快照缺 key_revocations(安全类)")
	}
	return nil
}

// payload 组装应用层投影；nil 价格/图片价目表示保留现状，显式空数组表示清空。
func (w *WireSnapshot) payload(tenants []Tenant) SnapshotPayload {
	payload := SnapshotPayload{
		Revocations: w.Sections.KeyRevocations,
		Tenants:     tenants,
		Topology:    emptyTopologyAsNil(w.Sections.Topology),
	}
	if w.Sections.Pricing != nil {
		payload.Pricing = w.Sections.Pricing.Pricing
		if payload.Pricing == nil {
			payload.Pricing = []PricingEntry{}
		}
	}
	if w.Sections.ImagePricing != nil {
		payload.ImagePricing = w.Sections.ImagePricing.Entries
		if payload.ImagePricing == nil {
			payload.ImagePricing = []ImagePricingEntry{}
		}
	}
	return payload
}

// emptyTopologyAsNil 空拓扑按缺席处理：控制面尚未配置时保留本地目录，
// 避免把「没有路由」误当成「下线全部路由」。
func emptyTopologyAsNil(topology *Topology) *Topology {
	if topology == nil {
		return nil
	}
	if len(topology.Providers) == 0 && len(topology.ModelRoutes) == 0 && len(topology.Models) == 0 {
		return nil
	}
	return topology
}

// requireSecuritySections 固定清单必须同时出现在声明的安全节与实体节。
func requireSecuritySections(declared []string) error {
	seen := make(map[string]bool, len(declared))
	for _, name := range declared {
		seen[name] = true
	}
	for _, name := range muniuSecuritySections {
		if !seen[name] {
			return fmt.Errorf("control: 快照缺安全节声明 %s", name)
		}
	}
	return nil
}

// tenants 把租户目录与权益配额合并为 muniu 的租户投影。
//
//nolint:cyclop // 配额维度白名单映射与租户目录合并是单一投影职责,拆分会割裂缺席/显式 0 的判定
func (w *WireSnapshot) tenants() ([]Tenant, error) {
	quotas := make(map[string]map[string]int64, len(w.Sections.Entitlements.Tenants))
	maxInflight := make(map[string]int64, len(w.Sections.Entitlements.Tenants))
	for _, e := range w.Sections.Entitlements.Tenants {
		values := make(map[string]int64, len(e.Quotas))
		for _, q := range e.Quotas {
			switch strings.ToLower(strings.TrimSpace(q.Dimension)) {
			case "rpm":
				values["rpm"] = q.Limit
			case "tpm":
				values["tpm"] = q.Limit
			case "daily_quota_credits":
				values["daily_quota_credits"] = q.Limit
			case "max_inflight":
				values["max_inflight"] = q.Limit
				maxInflight[e.TenantKey] = q.Limit
			}
		}
		quotas[e.TenantKey] = values
	}
	out := make([]Tenant, 0, len(w.Sections.TenantDirectory.Tenants))
	for _, t := range w.Sections.TenantDirectory.Tenants {
		name := strings.TrimSpace(t.Name)
		if name == "" {
			name = t.TenantKey
		}
		q := quotas[t.TenantKey]
		var maxInflightPtr *int64
		if limit, ok := maxInflight[t.TenantKey]; ok {
			if limit < 0 || int64(int(limit)) != limit {
				return nil, fmt.Errorf("control: 租户 %s max_inflight 超出本机范围", t.TenantKey)
			}
			maxInflightPtr = &limit
		}
		out = append(out, Tenant{
			ID:                t.ID,
			Name:              name,
			Status:            strings.ToLower(strings.TrimSpace(t.Status)),
			RPMLimit:          q["rpm"],
			TPMLimit:          q["tpm"],
			DailyQuotaCredits: q["daily_quota_credits"],
			MaxInflight:       maxInflightPtr,
		})
	}
	return out, nil
}
