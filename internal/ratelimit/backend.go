// SharedBackend = 限流计数的可插拔后端(ADR-0008 修订,cluster-ratelimit-shared-view):
// standalone 缺省 nil(Gate 走进程内 inmem 语义,逐字节零变更);cluster 配置 redis
// 后端(Lua 原子,共享视图消除 ×N;后端不可达降级回实例自治并告警——宁有弱限制,勿无限制)。
// 判定语义契约:超限拒 + Retry-After>0;TPM 前置判定(Allow)与完成记账(Record)分离,
// 与 inmem 同构;两后端经同一语义测试集对拍(semantics_test.go:判定布尔逐例一致,
// Retry-After 允许 ≤1s 数值差——令牌桶浮点路径与 GCRA 的有理差异上界)。
package ratelimit

import (
	"net/http"
	"time"

	"github.com/solvela/muniu/internal/auth"
)

// SharedBackend 共享计数后端(redis 形态);inmem 现状即 Gate 直引的 Limiter/TPM,无需适配。
type SharedBackend interface {
	// AllowRPM 判定放行;(false, retry) 时 retry 为建议等待时长。
	AllowRPM(keyID string, rpm int64, now time.Time) (bool, time.Duration)
	// AllowTPM 前置判定(窗口总量(含当前秒) < limit)。
	AllowTPM(keyID string, limit int64, now time.Time) bool
	// RecordTPM 请求完成后记账 token 数(≤0 忽略,与 inmem 同契约)。
	RecordTPM(keyID string, tokens int64, now time.Time)
	// Degraded 当前是否处于降级(后端不可达,回退实例自治)状态。
	Degraded() bool
	// Close 释放连接资源。
	Close() error
}

// TPMRecorder proxy 记账回填的最小接口(*TPM 与 SharedBackend 均满足)。
type TPMRecorder interface {
	Record(keyID string, tokens int64, now time.Time)
}

// sharedMiddleware 共享后端路径:链序契约不变(quota → RPM → TPM)——quota 闸
// 维持本地(日配额由控制面聚合 ingest 纠正,ADR-0008 修订维持该分工),
// RPM/TPM 两闸走共享视图。
func (g *Gate) sharedMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		info := auth.KeyFrom(r.Context())
		if info == nil {
			next.ServeHTTP(w, r)
			return
		}
		// 1) 日配额(key 级 + 租户聚合,本地)
		if g.Quota != nil && g.Quota.exceeded(keyInfoFrom(r.Context()), timeNow()) {
			g.denyQuota(w, r)
			return
		}
		now := timeNow()
		// 2) RPM 令牌桶(共享视图)
		if info.RPMLimit > 0 {
			if ok, retry := g.Shared.AllowRPM(info.ID, info.RPMLimit, now); !ok {
				g.deny429(w, r, retry)
				return
			}
		}
		// 3) TPM 滑动窗口(共享视图)
		if info.TPMLimit > 0 && !g.Shared.AllowTPM(info.ID, info.TPMLimit, now) {
			g.deny429(w, r, time.Second) // TPM 无 Retry-After 依据,给 1s
			return
		}
		next.ServeHTTP(w, r)
	})
}
