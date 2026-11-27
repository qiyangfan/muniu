// 服务只读摘要的路由计数：managed 快照优先于本地静态配置。
package main

import (
	"testing"

	"github.com/solvela/muniu/internal/config"
	"github.com/solvela/muniu/internal/control"
)

func TestEffectiveRouteCount_快照优先(t *testing.T) {
	cfg := &config.Config{ModelRoutes: map[string][]config.Candidate{
		"static-only": {{Provider: "p", Model: "m"}},
	}}
	if got := effectiveRouteCount(cfg, nil); got != 1 {
		t.Fatalf("无快照时应取静态配置: %d", got)
	}
	snap := &control.SnapshotEnvelope{Payload: control.SnapshotPayload{
		Topology: &control.Topology{ModelRoutes: map[string][]control.Route{
			"alias-a": {{Provider: "p", Model: "m"}},
			"alias-b": {{Provider: "p", Model: "m"}},
		}},
	}}
	if got := effectiveRouteCount(cfg, snap); got != 2 {
		t.Fatalf("managed 应取有效快照路由: %d", got)
	}
	snap.Payload.Topology = nil
	if got := effectiveRouteCount(cfg, snap); got != 1 {
		t.Fatalf("快照无拓扑投影时回落静态配置: %d", got)
	}
}
