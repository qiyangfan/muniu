// 模型发现(gateway-parity「model-discovery」):GET /v1/models 与 GET /v1/models/{model}。
// 目录 = 拓扑快照 model_routes 别名键集——与转发严格同源(热替换同步可见,零漂移机制)。
// 只读元数据面:零上游调用、零存储写路径、零计量、不接插件挂点;
// 数据面鉴权在装配层负责,配额/限流刻意不挂(配额耗尽的客户端恰需列模型诊断)。
package proxy

import (
	"encoding/json"
	"net/http"
	"sort"
	"time"

	"github.com/solvela/muniu/internal/logging"
	"github.com/solvela/muniu/internal/protocol"
)

// ownedBy 目录归属标注:别名是多候选换道语义,标注单一 provider 归属即失真,恒为网关自身。
const ownedBy = "muniu"

// modelEntry OpenAI /v1/models 条目形态;created = 快照加载时刻(进程内稳定,不承诺模型创建时间语义)。
type modelEntry struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

// ModelAliases 当前快照的别名目录(id 升序,输出确定)与快照加载时刻。
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

// HandlerModels 模型发现入口:路由注册为 GET /v1/models(列表)与 GET /v1/models/{model}(单查),
// 同一 handler 以 PathValue 分流;未命中按别名(路由键)判定,不匹配上游模型名。
func (p *Proxy) HandlerModels() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		aliases, loadedAt := p.ModelAliases()
		created := loadedAt.Unix()
		if id := r.PathValue("model"); id != "" {
			for _, a := range aliases {
				if a == id {
					writeModelsJSON(w, http.StatusOK, modelEntry{ID: id, Object: "model", Created: created, OwnedBy: ownedBy})
					return
				}
			}
			writeModelsJSON(w, http.StatusNotFound, protocol.NewErrorEnvelope(
				protocol.TypeNotFound, "model_not_found", "模型 "+id+" 不存在", logging.RequestIDFrom(r.Context())))
			return
		}
		data := make([]modelEntry, 0, len(aliases))
		for _, a := range aliases {
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
