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
				"providers": len(providers), "model_routes": routeCount(cfg),
			},
			"control": map[string]any{
				"snapshot": rt.SnapshotStatus(), "registration": rt.RegistrationStatus(),
				"commands": commandStats(ctx, rt),
			},
			"stale": rt.SnapshotStatus().Stale,
		}
		return out, nil
	}
}

// routeCount 路由别名数（本地配置；managed 拓扑投影可从快照覆盖）。
func routeCount(cfg *config.Config) int {
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
