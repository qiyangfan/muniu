// 服务只读摘要：供锦官只读投影展示 muniu 服务域配置与运行时状态；不含凭据。
package main

import (
	"context"
	"database/sql"
	"time"

	"github.com/solvela/muniu/internal/config"
	"github.com/solvela/muniu/internal/control"
	"github.com/solvela/muniu/internal/plugin"
	"github.com/solvela/muniu/internal/proxy"
	"github.com/solvela/muniu/internal/version"
)

// newServiceSummary 构造只读摘要闭包。
func newServiceSummary(db *sql.DB, rt *control.Runtime, cfg *config.Config, plugins *plugin.Registry,
	px *proxy.Proxy, started time.Time) func(context.Context) (map[string]any, error) {
	return func(ctx context.Context) (map[string]any, error) {
		if err := db.PingContext(ctx); err != nil {
			return nil, err
		}
		var tenants, keys, oauthAccounts, cacheEntries int64
		if err := db.QueryRowContext(ctx, `SELECT
			(SELECT COUNT(*) FROM tenants),
			(SELECT COUNT(*) FROM api_keys WHERE status='active'),
			(SELECT COUNT(*) FROM oauth_accounts),
			(SELECT COUNT(*) FROM semantic_cache)`).
			Scan(&tenants, &keys, &oauthAccounts, &cacheEntries); err != nil {
			return nil, err
		}
		providers, _ := px.Topology()
		mode := "standalone"
		if cfg.ControlPlane.Managed() {
			mode = "managed"
		}
		snapStatus := rt.SnapshotStatus()
		out := map[string]any{
			"source": "muniu", "service_id": control.ServiceID, "mode": mode,
			"version": version.Version, "instance_id": rt.InstanceID(),
			"runtime": map[string]any{
				"started_at":     started.UTC(),
				"uptime_seconds": int64(time.Since(started).Seconds()),
			},
			"domain": map[string]any{
				"tenants": tenants, "api_keys": keys, "oauth_accounts": oauthAccounts,
				"plugins": len(plugins.List()), "semantic_cache_entries": cacheEntries,
				"providers": len(providers), "model_routes": effectiveRouteCount(cfg, rt.LastSnapshot()),
			},
			"control": map[string]any{
				"snapshot": snapStatus, "registration": rt.RegistrationStatus(),
				"commands": commandStats(ctx, rt),
			},
			"stale": snapStatus.Stale,
		}
		return out, nil
	}
}

// effectiveRouteCount 运行态路由别名数：managed 以最近快照为准，standalone 用本地配置。
func effectiveRouteCount(cfg *config.Config, snap *control.SnapshotEnvelope) int {
	if snap != nil && snap.Payload.Topology != nil {
		return len(snap.Payload.Topology.ModelRoutes)
	}
	return len(cfg.ModelRoutes)
}

// commandStats 台账计数；失败按零。
func commandStats(ctx context.Context, rt *control.Runtime) map[string]int64 {
	stats, err := rt.CommandStats(ctx)
	if err != nil {
		return map[string]int64{"pending": 0, "acked": 0, "failed": 0}
	}
	return stats
}
