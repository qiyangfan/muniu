// 运行时页后端：控制面消费状态、共享控制只读投影、本地命令与最近日志。
// managed 下共享控制一律只读；本地命令不经锦官队列，锦官不可用仍可执行。
package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/solvela/muniu/internal/control"
	"github.com/solvela/muniu/internal/protocol"
	"github.com/solvela/muniu/internal/version"
)

// RuntimeProvider 控制面消费侧聚合接口，由 control.Runtime 实现。
type RuntimeProvider interface {
	SnapshotStatus() control.SnapshotStatus
	RegistrationStatus() control.RegistrationStatus
	CommandStats(ctx context.Context) (map[string]int64, error)
	Commands(ctx context.Context, limit int) ([]control.CommandView, error)
	ExecuteLocal(ctx context.Context, actor, reason, action string, params []byte) (control.CommandView, error)
	LastSnapshot() *control.SnapshotEnvelope
	InstanceID() string
	ServiceID() string
	StartedAt() time.Time
}

var _ RuntimeProvider = (*control.Runtime)(nil)

// runtimeStatus GET /admin/v1/runtime/status。
func runtimeStatus(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mode := "standalone"
		if d.Managed {
			mode = "managed"
		}
		out := map[string]any{
			"mode": mode, "managed": d.Managed,
			"service_id": control.ServiceID, "version": version.Version,
			"service_api_enabled": d.ServiceAPIEnabled,
			"observed_at":         time.Now().UTC(),
		}
		if d.Runtime != nil {
			out["instance_id"] = d.Runtime.InstanceID()
			out["started_at"] = d.Runtime.StartedAt().UTC()
			out["snapshot"] = d.Runtime.SnapshotStatus()
			out["registration"] = d.Runtime.RegistrationStatus()
			stats, err := d.Runtime.CommandStats(r.Context())
			if err != nil {
				stats = map[string]int64{"pending": 0, "acked": 0, "failed": 0}
			}
			out["commands"] = stats
		} else {
			out["instance_id"] = ""
			out["snapshot"] = control.SnapshotStatus{}
			out["registration"] = control.RegistrationStatus{State: "disabled"}
			out["commands"] = map[string]int64{"pending": 0, "acked": 0, "failed": 0}
		}
		writeJSON(w, 200, out)
	}
}

// runtimeSharedControl GET /admin/v1/runtime/shared-control：共享控制只读投影（不含凭据）。
func runtimeSharedControl(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		out := map[string]any{"managed": d.Managed, "snapshot": control.SnapshotStatus{}}
		if d.Runtime == nil {
			writeJSON(w, 200, out)
			return
		}
		st := d.Runtime.SnapshotStatus()
		out["snapshot"] = st
		last := d.Runtime.LastSnapshot()
		if last == nil {
			out["shared"] = map[string]any{"available": false}
			writeJSON(w, 200, out)
			return
		}
		out["shared"] = sharedProjection(last)
		writeJSON(w, 200, out)
	}
}

// sharedProjection 只读聚合计数，不返回配置明细。
func sharedProjection(env *control.SnapshotEnvelope) map[string]any {
	topo := map[string]any{"providers": 0, "routes": 0, "models": 0}
	if env.Payload.Topology != nil {
		routes := 0
		for _, cands := range env.Payload.Topology.ModelRoutes {
			routes += len(cands)
		}
		topo = map[string]any{
			"providers": len(env.Payload.Topology.Providers),
			"routes":    routes,
			"models":    len(env.Payload.Topology.Models),
		}
	}
	revVersion := int64(0)
	if env.Payload.Revocations != nil {
		revVersion = env.Payload.Revocations.Version
	}
	return map[string]any{
		"available": true, "version": env.Version, "observed_at": env.ObservedAt,
		"tenants": len(env.Payload.Tenants), "pricing": len(env.Payload.Pricing),
		"topology": topo, "revocations_version": revVersion,
		"policies_present": env.Payload.Policies != nil,
	}
}

// runtimeCommands GET /admin/v1/runtime/commands。
func runtimeCommands(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.Runtime == nil {
			writeJSON(w, 200, map[string]any{"enabled": false, "commands": []any{},
				"stats": map[string]int64{"pending": 0, "acked": 0, "failed": 0}})
			return
		}
		cmds, err := d.Runtime.Commands(r.Context(), 50)
		if err != nil {
			respondErr(d, w, r, err, "commands_list_failed")
			return
		}
		stats, err := d.Runtime.CommandStats(r.Context())
		if err != nil {
			stats = map[string]int64{"pending": 0, "acked": 0, "failed": 0}
		}
		writeJSON(w, 200, map[string]any{"enabled": true, "commands": cmds, "stats": stats})
	}
}

// runtimeCommandExecute POST /admin/v1/runtime/commands：本地命令直接执行。
func runtimeCommandExecute(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.Runtime == nil {
			writeAdminError(w, r, 503, protocol.TypeUnavailable, "runtime_disabled", "运行时命令未装配")
			return
		}
		var req struct {
			Action string          `json:"action"`
			Params json.RawMessage `json:"params"`
			Reason string          `json:"reason"`
		}
		if err := decode(r, &req); err != nil || strings.TrimSpace(req.Action) == "" {
			writeAdminError(w, r, 400, protocol.TypeBadRequest, "bad_request", "action 必填")
			return
		}
		if len(req.Reason) > 500 {
			writeAdminError(w, r, 400, protocol.TypeBadRequest, "bad_request", "reason 过长(≤500)")
			return
		}
		params := req.Params
		if len(params) == 0 {
			params = json.RawMessage(`{}`)
		}
		actor := ActorFrom(r.Context())
		name := actor.Name
		if name == "" {
			name = "admin"
		}
		view, err := d.Runtime.ExecuteLocal(r.Context(), name, req.Reason, req.Action, params)
		if err != nil {
			writeAdminError(w, r, 400, protocol.TypeBadRequest, "command_failed", err.Error())
			return
		}
		writeJSON(w, 200, view)
	}
}

// runtimeLogs GET /admin/v1/runtime/logs：最近进程日志（已脱敏）。
func runtimeLogs(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		limit := 100
		if raw := r.URL.Query().Get("limit"); raw != "" {
			if n := parsePositiveInt(raw); n > 0 && n <= 200 {
				limit = n
			}
		}
		entries := []any{}
		if d.Logs != nil {
			for _, e := range d.Logs(limit) {
				entries = append(entries, e)
			}
		}
		writeJSON(w, 200, map[string]any{"entries": entries})
	}
}

// parsePositiveInt 解析正整数；失败返回 0。
func parsePositiveInt(raw string) int {
	n := 0
	for _, ch := range raw {
		if ch < '0' || ch > '9' {
			return 0
		}
		n = n*10 + int(ch-'0')
		if n > 1<<20 {
			return 0
		}
	}
	return n
}
