// 亲和表将成功响应 ID 绑定到 provider 和创建请求的 key，条目保存在进程内并受 TTL、LRU 容量限制。
// 有状态请求只在绑定的 provider 内换道；亲和未知或失效时返回自解释错误，不盲发上游。
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

// 亲和表的 TTL 和容量上限。
const (
	affinityTTL      = 24 * time.Hour
	affinityCapacity = 4096
)

// responseAffinityLostCode 表示响应 ID 的亲和关系未知或已失效。
const responseAffinityLostCode = "response_affinity_lost"

type affEntry struct {
	id       string
	provider string
	apiKeyID string // 创建该响应的请求所用 key ID
	at       time.Time
}

// affinityTable 保存响应 ID 的 provider 亲和关系。
type affinityTable struct {
	mu      sync.Mutex
	entries map[string]*list.Element // respID → 元素
	lru     *list.List               // 前端=最近使用
}

func newAffinityTable() *affinityTable {
	return &affinityTable{entries: map[string]*list.Element{}, lru: list.New()}
}

// Put 记录响应 ID 的 provider 和创建它的 key；相同 ID 会覆盖旧记录。
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

// Get 查询响应 ID 的亲和关系；过期、缺少或不匹配 key ID 都按未知处理，避免泄露 ID 是否存在。
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

// apiKeyIDOf 返回当前请求的鉴权 key ID；缺少鉴权信息时返回空串。
func apiKeyIDOf(r *http.Request) string {
	if info := auth.KeyFrom(r.Context()); info != nil {
		return info.ID
	}
	return ""
}

// stickyError 保存粘性路由失败时返回的错误。
type stickyError struct{ code, msg string }

// stickyRoute 将有状态请求限制在亲和 provider 的候选内；亲和未知或失效时返回错误。
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
		// 拓扑变更后亲和 provider 不再可选，按亲和失效处理。
		StickyRequestsTotal.Inc("lost")
		return nil, &stickyError{
			code: responseAffinityLostCode,
			msg:  "粘性状态失效:该 previous_response_id 的来源已不在当前候选列表,请改发完整 input 重建上下文",
		}
	}
	return out, nil
}
