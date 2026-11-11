// 同构直通路由：每 attempt 判定直通或翻译路径，决策点唯一；入站原生上下文经 request
// context 传递；含惰性拒绝升格与直通回落纪律。canonical 是权威请求形态，原始体仅供直通保真出站。
package proxy

import (
	"context"
	"net/http"

	"github.com/solvela/muniu/internal/adapter"
	"github.com/solvela/muniu/internal/config"
	"github.com/solvela/muniu/internal/failover"
	"github.com/solvela/muniu/internal/protocol"
)

// inboundNative 入站原生上下文：handler 保留原始体后经 request context 传入管道，
// chat 入站无此上下文。
type inboundNative struct {
	proto      protocol.InboundProtocol
	raw        []byte // 客户端原始请求体，逐字节，仅直通出站消费
	model      string // 路由别名，失格时经轻量解析仍可得
	stream     bool
	prevID     string // responses 有状态请求的 previous_response_id，粘性路由载体
	ineligCode string // 翻译失格机器码，非空即 canonical 不可得，直通专属或升格 400
	ineligMsg  string // 升格 400 时的文案，与翻译期拒绝保持一致
}

func (n *inboundNative) sticky() bool { return n != nil && n.prevID != "" }

type inboundKey struct{}

// withInbound 把原生上下文挂上请求，即 clone 后的 r2。
func withInbound(r *http.Request, n *inboundNative) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), inboundKey{}, n))
}

// inboundFrom 取原生上下文；chat 入站返回 nil。
func inboundFrom(r *http.Request) *inboundNative {
	n, _ := r.Context().Value(inboundKey{}).(*inboundNative)
	return n
}

// laneKey 入站 lane 标记：不携带原生上下文的 lane(gemini)经此声明身份,chat 裸入站
// 无标记(零值)。公共管道内 lane 相关判定(亲和范围/oauth 收窄)同源消费,防再劈叉。
type laneKey struct{}

// withLane 标记请求的入站 lane(gemini handler 委托 chat 管道前挂载)。
func withLane(r *http.Request, proto protocol.InboundProtocol) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), laneKey{}, proto))
}

// laneFrom 取 lane 标记;无标记(裸 chat)返回零值。
func laneFrom(r *http.Request) protocol.InboundProtocol {
	p, _ := r.Context().Value(laneKey{}).(protocol.InboundProtocol)
	return p
}

// pathModeWriter 响应 writer 的直通/翻译模式位：直通为恒等写，翻译为反向映射。
// 切换仅发生在首字节提交前，tw.committed 不变式由候选循环保证；
// 已提交后绝不二次写出、换道或混用形态。
type pathModeWriter interface{ setNativeMode(bool) }

// setWriterNativeMode 把模式位穿透 trackWriter 送达协议 wrapper，无 wrapper 时零开销。
func setWriterNativeMode(w http.ResponseWriter, native bool) {
	if pw, ok := w.(pathModeWriter); ok {
		pw.setNativeMode(native)
	}
}

// attemptPath 每 attempt 路径判定结果，判定点唯一。
type attemptPath struct {
	native   bool
	preparer adapter.NativePreparer // native=true 时非 nil
	fallback string                 // 回落原因，结构化日志留痕；非空时 native=false
}

// decideNativePath 直通/翻译判定，纯函数可表驱动：
// ①chat 入站恒翻译，基座本就透传无直通增益；②候选缺直通能力时翻译，能力断言结合声明表，
// 与换道跨协议边界同一处理；③插件已改写 canonical 时原始体过期，回落翻译。
// 治理干预回落不在判定内，它依赖候选能力目录，在 attempt 内评估后并入同一决策点出口。
func decideNativePath(nat *inboundNative, adapters map[string]adapter.Adapter,
	nativeResps map[string]bool, cand failover.Candidate, pluginModified bool) attemptPath {
	if nat == nil {
		return attemptPath{}
	}
	ad, ok := adapters[cand.Provider]
	if !ok {
		return attemptPath{}
	}
	np, ok := ad.(adapter.NativePreparer)
	if !ok {
		return attemptPath{}
	}
	// 协议身份匹配：适配器原生协议不等于入站协议则翻译，与换道跨协议边界同一处理
	if np.NativeProto() != nat.proto {
		return attemptPath{}
	}
	if nat.proto == protocol.InboundResponses {
		// 声明制：native_responses 显式声明才直通，OpenAI 兼容不代表 responses 端点可用
		if !nativeResps[cand.Provider] {
			return attemptPath{}
		}
	}
	if pluginModified {
		return attemptPath{fallback: "plugin_modified"}
	}
	return attemptPath{native: true, preparer: np}
}

// buildNativeResps 快照声明表，provider 到 native_responses 的映射，装配与热替换共用。
func buildNativeResps(providers []config.Provider) map[string]bool {
	m := make(map[string]bool, len(providers))
	for _, p := range providers {
		m[p.Name] = p.NativeResponses
	}
	return m
}

// aliasHasNative 别名级预判：别名是否含该入站协议的直通资格候选，
// 作为惰性拒绝的即时 400 判据，无直通候选的别名保持既有即时拒绝时序。
func aliasHasNative(snap *topologySnapshot, proto protocol.InboundProtocol, cands []config.Candidate) bool {
	for _, c := range cands {
		if path := decideNativePath(&inboundNative{proto: proto}, snap.adapters, snap.nativeResps,
			failover.Candidate{Provider: c.Provider}, false); path.native {
			return true
		}
	}
	return false
}

// upgradeIneligible 惰性拒绝升格：翻译路径实际被需要时，在该尝试出站前升格为既有 400 信封，
// 文案与形态同翻译期拒绝保持一致。
// 信封走 writer 直通模式恒等写出，绕开协议 wrapper 的反向映射，形态保真。
func (p *Proxy) upgradeIneligible(w http.ResponseWriter, r *http.Request, nat *inboundNative) {
	if pw, ok := w.(pathModeWriter); ok {
		pw.setNativeMode(true)
	}
	switch nat.proto {
	case protocol.InboundMessages:
		writeMessagesError(w, http.StatusBadRequest, "invalid_request_error", nat.ineligMsg)
	default:
		p.writeError(w, r, protocol.TypeBadRequest, nat.ineligCode, nat.ineligMsg)
	}
}
