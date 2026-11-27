// SharedBackend 限流计数的共享后端：standalone 缺省 nil，Gate 走进程内 inmem 语义；
// cluster 配置 redis 后端（Lua 原子，共享计数消除 ×N 放大），后端不可达即降级回
// 实例自治并告警——宁有弱限制，勿无限制。
// 判定语义：超限拒且 Retry-After>0；TPM 前置判定（Allow）与完成记账（Record）分离，
// 与 inmem 同构；两后端经同一语义测试集对拍（semantics_test.go）：判定布尔逐例一致，
// Retry-After 允许 ≤1s 数值差（令牌桶浮点路径与 GCRA 有理差异的上界）。
package ratelimit

import (
	"time"
)

// SharedBackend 共享计数后端（redis 形态）；inmem 现状即 Gate 直引的 Limiter/TPM，无需适配。
type SharedBackend interface {
	// AllowRPM 判定放行，拒绝时 retry 为建议等待时长。
	AllowRPM(keyID string, rpm int64, now time.Time) (bool, time.Duration)
	// AllowTPM 前置判定：窗口总量（含当前秒）< limit。
	AllowTPM(keyID string, limit int64, now time.Time) bool
	// RecordTPM 请求完成后记账 token 数，≤0 忽略（与 inmem 同契约）。
	RecordTPM(keyID string, tokens int64, now time.Time)
	// Degraded 当前是否处于降级态（后端不可达，回退实例自治）。
	Degraded() bool
	// Close 释放连接资源。
	Close() error
}

// TPMRecorder proxy 记账回填的最小接口，*TPM 与 SharedBackend 均满足。
type TPMRecorder interface {
	Record(keyID string, tokens int64, now time.Time)
}

// TenantTPMRecorder 租户聚合记账接口；proxy 检测到实现时同时回填租户窗口。
type TenantTPMRecorder interface {
	RecordTenant(tenantID string, tokens int64, now time.Time)
}

// CompositeRecorder 同时回填 key 与租户窗口；两项均可为 nil（零行为）。
type CompositeRecorder struct {
	Key    TPMRecorder
	Tenant TenantTPMRecorder
}

// Record key 级记账。
func (c CompositeRecorder) Record(keyID string, tokens int64, now time.Time) {
	if c.Key != nil {
		c.Key.Record(keyID, tokens, now)
	}
}

// RecordTenant 租户级聚合记账。
func (c CompositeRecorder) RecordTenant(tenantID string, tokens int64, now time.Time) {
	if c.Tenant != nil {
		c.Tenant.RecordTenant(tenantID, tokens, now)
	}
}
