// key 级模型白名单数据面执行(key-model-allowlist):
// 校验点 = 每条 lane 的模型别名解析之后、语义缓存检索与任何上游调用之前——被禁别名
// 不得吃缓存回放(回放即事实放行);插件改写 model 的重解析路径必须再校验(改写不得
// 绕过白名单)。KeyInfo 缺失(直挂 handler 的测试路径)= 不限,与空白名单同语义。
package proxy

import (
	"net/http"

	"github.com/solvela/muniu/internal/auth"
	"github.com/solvela/muniu/internal/protocol"
)

// modelAllowedFor 白名单判定:空白名单恒放行;有白名单时线性扫描
// (基数个位,零分配不建 map——请求热路径,分配比比较贵)。
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

// enforceModelAllowlist 白名单闸:放行返回 true;拒绝时已写出 403 forbidden/model_not_allowed
// 信封(上游零调用、语义缓存零检索、零计量落账)。访问拒绝与请求畸形必须可区分
// (conventions「错误信封」:客户端据此程序化引导,而非改参数)。
func (p *Proxy) enforceModelAllowlist(w http.ResponseWriter, r *http.Request, alias string) bool {
	if modelAllowedFor(auth.KeyFrom(r.Context()), alias) {
		return true
	}
	p.writeError(w, r, protocol.TypeForbidden, "model_not_allowed", "当前 key 无权访问模型 "+alias)
	return false
}
