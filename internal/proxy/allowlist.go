// 每条请求在模型别名确定后、缓存或上游调用前校验 key 白名单；插件改写模型名后也要重校验。
// 缺少 KeyInfo 时按未设置白名单处理。
package proxy

import (
	"net/http"

	"github.com/solvela/muniu/internal/auth"
	"github.com/solvela/muniu/internal/protocol"
)

// modelAllowedFor 判断别名是否在 key 白名单中；名单很小时线性扫描可避免额外分配。
func modelAllowedFor(key *auth.KeyInfo, alias string) bool {
	if key == nil || len(key.AllowedModels) == 0 {
		return true
	}
	for _, a := range key.AllowedModels {
		if a == alias {
			return true
		}
	}
	return false
}

// enforceModelAllowlist 放行时返回 true；拒绝时写出 403 forbidden/model_not_allowed 信封。
func (p *Proxy) enforceModelAllowlist(w http.ResponseWriter, r *http.Request, alias string) bool {
	if modelAllowedFor(auth.KeyFrom(r.Context()), alias) {
		return true
	}
	p.writeError(w, r, protocol.TypeForbidden, "model_not_allowed", "当前 key 无权访问模型 "+alias)
	return false
}
