// 插件请求体钩子挂点：链序在限流之后、语义缓存与上游候选循环之前，单次执行。
// 语义缓存检索在钩子之后：脱敏必须发生在缓存键计算之前，敏感载荷不得进语义缓存、
// 不得进计量 inputText。插件禁用(p.plugins == nil)时零分配短路；
// 注册表为空时仅一次空快照且无堆分配，改写未发生则不做重解析。
package proxy

import (
	"net/http"
	"strings"

	"github.com/solvela/muniu/internal/plugin"
	"github.com/solvela/muniu/internal/protocol"
)

// PluginModifiedHeader 改写留痕响应头，值为逗号分隔的触发改写插件名。
const PluginModifiedHeader = "X-Muniu-Plugin"

// applyPlugins 对 body 执行启用插件链，返回执行结论与请求是否已终结。
// 拒绝走 bad_request/plugin_rejected，fail_closed containment 走 internal/plugin_failed。
// 改写产物由调用方重解析，产物非法即插件 bug，按请求级失败拒绝，fail_closed。
func (p *Proxy) applyPlugins(tw http.ResponseWriter, r *http.Request, body []byte) (plugin.ApplyResult, bool) {
	if p.plugins == nil {
		return plugin.ApplyResult{Body: body}, false // 禁用态零开销短路，无快照无分配
	}
	res := p.plugins.ApplyToBody(r.Context(), body)
	if res.RejectedBy != "" {
		p.writeError(tw, r, protocol.TypeBadRequest, "plugin_rejected", "请求被插件 "+res.RejectedBy+" 拒绝: "+res.Reason)
		return res, true
	}
	if res.FailedBy != "" {
		p.writeError(tw, r, protocol.TypeInternal, "plugin_failed",
			"插件 "+res.FailedBy+" 执行失败,已按安全策略阻断请求")
		return res, true
	}
	return res, false
}

// pluginBodyHook 插件请求体钩子的公共骨架：无插件空短路、执行、被拒/失败即终结。
// outcomes 提供协议专属的成功收尾：重解析、重路由与留痕；返回 done=true 表示请求已终结。
func (p *Proxy) pluginBodyHook(tw http.ResponseWriter, r *http.Request, body []byte,
	onModified func(res plugin.ApplyResult) bool) bool {
	if p.plugins == nil {
		return false // 禁用态零开销短路，无快照无分配
	}
	res, done := p.applyPlugins(tw, r, body)
	if done {
		return true
	}
	if len(res.ModifiedBy) == 0 {
		return false
	}
	return onModified(res)
}

// rerouteAfterBodyRewrite 插件改写产物重解析后按新别名重路由并复核 key 级白名单。
// parse 为协议专属解析器；applied 仅在全部复核通过后回调，用于回写调用方的请求与候选。
func (p *Proxy) rerouteAfterBodyRewrite(tw http.ResponseWriter, r *http.Request, topo *topologySnapshot,
	res plugin.ApplyResult, parseErrMsg string, parse func([]byte) (model string, err error),
	applied func(model string) bool) bool {
	model, err := parse(res.Body)
	if err != nil {
		p.writeError(tw, r, protocol.TypeBadRequest, "plugin_rejected", parseErrMsg)
		return true
	}
	if _, ok := topo.routes[model]; !ok {
		p.writeError(tw, r, protocol.TypeBadRequest, "model_not_found", "模型别名不存在: "+model)
		return true
	}
	// key 级模型白名单闸之二：插件改写 model 后以最终出站别名为准再校验
	if !p.enforceModelAllowlist(tw, r, model) {
		return true
	}
	p.markPluginModified(tw, r, res.ModifiedBy)
	applied(model)
	return false
}

// rewriteModelHook 请求体插件钩子的整链：执行、重解析取 model、重路由复核，成功时 applied 回写。
// 返回 failed=true 表示请求已终结。
func (p *Proxy) rewriteModelHook(tw http.ResponseWriter, r *http.Request, topo *topologySnapshot,
	body []byte, parseErrMsg string, parse func([]byte) (string, error), applied func(model string) bool) bool {
	return p.pluginBodyHook(tw, r, body, func(res plugin.ApplyResult) bool {
		return p.rerouteAfterBodyRewrite(tw, r, topo, res, parseErrMsg, parse, applied)
	})
}

// markPluginModified 留痕头与审计日志：必须在改写产物重解析与重路由通过之后调用，
// 产物被拒的 400 响应不得携带留痕头，避免改写已生效的歧义，不利排障。
func (p *Proxy) markPluginModified(tw http.ResponseWriter, r *http.Request, modified []string) {
	if len(modified) == 0 {
		return
	}
	tw.Header().Set(PluginModifiedHeader, strings.Join(modified, ","))
	p.logger.Info("插件改写请求体",
		"request_id", loggingRequestID(r.Context()), "plugins", strings.Join(modified, ","))
}
