// 模型目录提供 GET /v1/models 和单模型查询，别名来自当前拓扑快照。
// 查询应用 key 白名单，不调用上游、不落库、不计量，也不受限流和配额拦截。
package proxy

import (
	"encoding/json"
	"net/http"
	"sort"
	"time"

	"github.com/solvela/muniu/internal/auth"
	"github.com/solvela/muniu/internal/logging"
	"github.com/solvela/muniu/internal/protocol"
)

// 别名可能路由到多个 provider，因此目录归属标记为网关自身。
const ownedBy = "muniu"

// modelEntry 是 OpenAI /v1/models 的条目形态；created 表示拓扑快照加载时间。
type modelEntry struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

// ModelAliases 返回按 ID 排序的当前模型别名和快照加载时间。
func (p *Proxy) ModelAliases() ([]string, time.Time) {
	s := p.topo.Load()
	if s == nil {
		return nil, time.Time{}
	}
	out := make([]string, 0, len(s.routes))
	for alias := range s.routes {
		out = append(out, alias)
	}
	sort.Strings(out)
	return out, s.loadedAt
}

// HandlerModels 处理模型列表和单模型查询；单模型按路由别名查找。
// 列表按请求 key 白名单过滤；单查对未配置别名返回 404，对白名单外别名返回 403。
func (p *Proxy) HandlerModels() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		aliases, loadedAt := p.ModelAliases()
		created := loadedAt.Unix()
		key := auth.KeyFrom(r.Context())
		if id := r.PathValue("model"); id != "" {
			known := false
			for _, a := range aliases {
				if a == id {
					known = true
					break
				}
			}
			if !known {
				writeModelsJSON(w, http.StatusNotFound, protocol.NewErrorEnvelope(
					protocol.TypeNotFound, "model_not_found", "模型 "+id+" 不存在", logging.RequestIDFrom(r.Context())))
				return
			}
			if !modelAllowedFor(key, id) {
				writeModelsJSON(w, http.StatusForbidden, protocol.NewErrorEnvelope(
					protocol.TypeForbidden, "model_not_allowed", "当前 key 无权访问模型 "+id, logging.RequestIDFrom(r.Context())))
				return
			}
			writeModelsJSON(w, http.StatusOK, modelEntry{ID: id, Object: "model", Created: created, OwnedBy: ownedBy})
			return
		}
		// 白名单过滤视图：交集保持 id 升序，全局目录本就升序，过滤保序
		view := aliases
		if key != nil && len(key.AllowedModels) > 0 {
			allowed := make(map[string]struct{}, len(key.AllowedModels))
			for _, a := range key.AllowedModels {
				allowed[a] = struct{}{}
			}
			out := make([]string, 0, len(aliases))
			for _, a := range aliases {
				if _, ok := allowed[a]; ok {
					out = append(out, a)
				}
			}
			view = out
		}
		data := make([]modelEntry, 0, len(view))
		for _, a := range view {
			data = append(data, modelEntry{ID: a, Object: "model", Created: created, OwnedBy: ownedBy})
		}
		writeModelsJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
	})
}

func writeModelsJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
