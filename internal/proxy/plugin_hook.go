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
