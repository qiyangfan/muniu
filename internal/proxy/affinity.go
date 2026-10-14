// 有状态粘性路由亲和表(protocol-native-passthrough;ADR-0014):
// 成功响应 id → provider,进程内 TTL + LRU 容量上限。粘性是服务端状态定向单上游的
// 显式语义(物理约束而非实现缺陷):粘性请求候选域收缩至亲和 provider——域内 key/
// 候选 failover 照常,跨 provider 换道不发生;亲和未知(重启丢失/非本网关创建)走
// 自解释降级,禁止盲发上游引发 404 换道级联。集群共享后端形态按 ADR-0008 复用登记,
// 未配共享 = 实例内粘性(HANDOFF 登记)。
package proxy

import (
	"container/list"
	"net/http"
	"sync"
	"time"

	"github.com/solvela/muniu/internal/auth"
	"github.com/solvela/muniu/internal/config"
	"github.com/solvela/muniu/internal/protocol"
)

// 亲和表运行参数(最小形态;容量/TTL 防无限增长,非配置面——契约常量)。
const (
	affinityTTL      = 24 * time.Hour
	affinityCapacity = 4096
)

// responseAffinityLostCode 亲和未知自解释机器码(区别于 previous_response_id_unsupported:
// 后者=别名无原生 lane,前者=lane 存在但绑定丢失)。
const responseAffinityLostCode = "response_affinity_lost"

type affEntry struct {
	id       string
	provider string
	apiKeyID string // 归属 key(responses-state-ownership):创建请求的鉴权 key 标识
	at       time.Time
}

// affinityTable 进程内亲和表:map + LRU 链(命中/写入提升前端,容量淘汰尾端,TTL 读取侧过滤)。
type affinityTable struct {
	mu      sync.Mutex
	entries map[string]*list.Element // respID → 元素
	lru     *list.List               // 前端=最近使用
}

func newAffinityTable() *affinityTable {
	return &affinityTable{entries: map[string]*list.Element{}, lru: list.New()}
}

// Put 记录/刷新响应 id → provider + 归属 key(成功响应回写;重复 id 覆盖并提升)。
// apiKeyID 为创建请求的鉴权 key 标识(auth 中间件保证非空;测试直挂路径可能为空,
// 空归属条目在 Get 侧永不匹配——fail-closed)。
func (a *affinityTable) Put(id, provider, apiKeyID string) {
	if id == "" || provider == "" {
		return
	}
	now := time.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	if e, ok := a.entries[id]; ok {
		e.Value.(*affEntry).provider = provider
		e.Value.(*affEntry).apiKeyID = apiKeyID
		e.Value.(*affEntry).at = now
		a.lru.MoveToFront(e)
		return
	}
	a.entries[id] = a.lru.PushFront(&affEntry{id: id, provider: provider, apiKeyID: apiKeyID, at: now})
	for a.lru.Len() > affinityCapacity {
		back := a.lru.Back()
		if back == nil {
			break
		}
		a.lru.Remove(back)
		delete(a.entries, back.Value.(*affEntry).id)
	}
}

// Get 查亲和(命中提升位置;过期即删,视为未知)。归属判定在表内单点完成:
// apiKeyID 为空(无鉴权信息,生产不可达)或与记录不符一律 !ok——三消费点对
// 「未知/不匹配」不可区分处理,不泄露存在性(responses-state-ownership)。
func (a *affinityTable) Get(id, apiKeyID string) (string, bool) {
	if apiKeyID == "" {
		return "", false
	}
	now := time.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	e, ok := a.entries[id]
	if !ok {
		return "", false
	}
	entry := e.Value.(*affEntry)
	if now.Sub(entry.at) > affinityTTL || entry.apiKeyID != apiKeyID {
		if now.Sub(entry.at) > affinityTTL {
			a.lru.Remove(e)
			delete(a.entries, id)
		}
		return "", false
	}
	a.lru.MoveToFront(e)
	entry.at = now
	return entry.provider, true
}

// apiKeyIDOf 当前请求的鉴权 key 标识(亲和归属判定;无鉴权信息返回空=永不匹配)。
func apiKeyIDOf(r *http.Request) string {
	if info := auth.KeyFrom(r.Context()); info != nil {
		return info.ID
	}
	return ""
}

// stickyError 粘性路由的即时终态错误(自解释信封;禁盲发上游)。
type stickyError struct{ code, msg string }

// stickyRoute 有状态请求的候选域收缩:
// ①别名无原生 responses 候选 → 翻译路径维持既有拒绝(chat 上游无服务端状态);
// ②亲和已知 → 候选收缩至亲和 provider(域内 failover 照常,跨 provider 不换道);
// ③亲和未知/亲和 provider 已离开候选 → 自解释降级。
func (p *Proxy) stickyRoute(r *http.Request, nat *inboundNative, topo *topologySnapshot, cands []config.Candidate) ([]config.Candidate, *stickyError) {
	if !aliasHasNative(topo, protocol.InboundResponses, cands) {
		StickyRequestsTotal.Inc("unsupported")
		return nil, &stickyError{
			code: protocol.CodePreviousResponseIDUnsupported,
			msg:  responsesRejectMessage(protocol.CodePreviousResponseIDUnsupported),
		}
	}
	provider, ok := p.affinity.Get(nat.prevID, apiKeyIDOf(r))
	if !ok {
		StickyRequestsTotal.Inc("lost")
		p.logger.Warn("粘性亲和未知,自解释降级(不盲发上游)",
			"request_id", loggingRequestID(r.Context()), "previous_response_id", nat.prevID)
		return nil, &stickyError{
			code: responseAffinityLostCode,
			msg:  "粘性状态丢失:网关未记录该 previous_response_id 的来源(可能因重启丢失或非本网关创建),请改发完整 input 重建上下文",
		}
	}
	StickyRequestsTotal.Inc("hit")
	var out []config.Candidate
	for _, c := range cands {
		if c.Provider == provider {
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		// 亲和 provider 已不在别名候选内(拓扑变更):同自解释降级,不盲发
		StickyRequestsTotal.Inc("lost")
		return nil, &stickyError{
			code: responseAffinityLostCode,
			msg:  "粘性状态失效:该 previous_response_id 的来源已不在当前候选列表,请改发完整 input 重建上下文",
		}
	}
	return out, nil
}
