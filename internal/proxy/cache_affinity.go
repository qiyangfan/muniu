// 会话缓存亲和(ADR-0020):同一会话健康前提下粘住同渠道/key,保上游前缀缓存。
// 表独立于 responses 状态亲和(生命周期/清除条件完全不同);条目绑 apiKeyID(归属隔离,
// 沿 responses-state-ownership 先例)。worth 闸门:仅缓存读>0 的成功 attempt 建粘;
// 失败/换道即清;池冷却/熔断让位但条目保留(恢复即复粘)。
package proxy

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/solvela/muniu/internal/metrics"
	"github.com/solvela/muniu/internal/protocol"
)

// TTL 与容量对齐 responses 亲和表先例(固定值起步,per-vendor 冷却时长待实测数据,
// design「后续可调」登记);进程内存态不落盘,重启归零由 worth 闸门自然重建。
const (
	cacheAffinityTTL      = 10 * time.Minute
	cacheAffinityCapacity = 4096
	sessionHeader         = "X-Session-Id"
)

// cacheAffinityRequests 亲和判定计数:result=hit(粘住)|miss(无条目)|stale(过期或 stick 不健康转原序)。
var cacheAffinityRequests = metrics.NewCounterVec("muniu_cache_affinity_total",
	"会话缓存亲和判定计数", "result")

type cacheAffinityEntry struct {
	sessionID       string
	provider, keyID string
	apiKeyID        string
	at              time.Time
}

// cacheAffinityTable 会话→(渠道,key) 亲和表:LRU+TTL,进程内存态。
type cacheAffinityTable struct {
	mu      sync.Mutex
	entries map[string]*list.Element
	lru     *list.List
}

func newCacheAffinityTable() *cacheAffinityTable {
	return &cacheAffinityTable{entries: map[string]*list.Element{}, lru: list.New()}
}

// note 建立/续期粘性(worth 闸门由调用方把关:缓存读>0 才进来);同会话覆盖旧值。
func (t *cacheAffinityTable) note(sessionID, provider, keyID, apiKeyID string) {
	if sessionID == "" || provider == "" || keyID == "" {
		return
	}
	now := time.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	if e, ok := t.entries[sessionID]; ok {
		en := e.Value.(*cacheAffinityEntry)
		en.provider, en.keyID, en.apiKeyID, en.at = provider, keyID, apiKeyID, now
		t.lru.MoveToFront(e)
		return
	}
	t.entries[sessionID] = t.lru.PushFront(&cacheAffinityEntry{
		sessionID: sessionID, provider: provider, keyID: keyID, apiKeyID: apiKeyID, at: now,
	})
	for t.lru.Len() > cacheAffinityCapacity {
		back := t.lru.Back()
		if back == nil {
			break
		}
		en := back.Value.(*cacheAffinityEntry)
		delete(t.entries, en.sessionID)
		t.lru.Remove(back)
	}
}

// lookup 查亲和:命中返回 (provider, keyID, "hit");过期返回 ("","", "stale") 并删条;
// 归属不匹配(apiKeyID 不同)按 miss 语义返回 "", "", "miss"(不可见,不泄露存在性)。
func (t *cacheAffinityTable) lookup(sessionID, apiKeyID string, now time.Time) (string, string, string) {
	if sessionID == "" {
		return "", "", "miss"
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	e, ok := t.entries[sessionID]
	if !ok {
		return "", "", "miss"
	}
	en := e.Value.(*cacheAffinityEntry)
	if now.Sub(en.at) > cacheAffinityTTL {
		t.lru.Remove(e)
		delete(t.entries, sessionID)
		return "", "", "stale"
	}
	if en.apiKeyID != apiKeyID {
		return "", "", "miss"
	}
	t.lru.MoveToFront(e)
	return en.provider, en.keyID, "hit"
}

// drop 清除粘性:本次尝试在 stick 键上失败或发生换道即调(缓存前提已破);换道清条,
// 冷却让位不清(恢复即复粘)。
func (t *cacheAffinityTable) drop(sessionID string) {
	if sessionID == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if e, ok := t.entries[sessionID]; ok {
		t.lru.Remove(e)
		delete(t.entries, sessionID)
	}
}

// sessionStickID 会话身份:自报头优先(只在 apiKeyID 域内解析);缺省 apiKeyID+首条
// user 消息文本哈希——取 user 而非 system:Agent 中途插改 system(提醒/压缩摘要)是常态,
// 首条 user 消息跨轮不变。无 user 消息返回空(整请求不参与亲和,不查不写)。
func sessionStickID(r *http.Request, apiKeyID string, req *protocol.ChatRequest) string {
	if sid := strings.TrimSpace(r.Header.Get(sessionHeader)); sid != "" {
		return "h:" + apiKeyID + ":" + sid
	}
	for _, msg := range req.Messages {
		if msg.Role != "user" {
			continue
		}
		if text := userTextOf(msg.Content); text != "" {
			sum := sha256.Sum256([]byte(apiKeyID + "\x00" + text))
			return "u:" + hex.EncodeToString(sum[:6])
		}
		return "" // 首条 user 无文本(纯多模态图像等):不亲和
	}
	return ""
}

// userTextOf 提取消息文本:字符串原样;多模态数组取 text 片段按序拼接;形态不明返回空。
func userTextOf(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err == nil {
		var b strings.Builder
		for _, pt := range parts {
			if pt.Type == "text" {
				b.WriteString(pt.Text)
			}
		}
		return b.String()
	}
	return ""
}

// extractCacheRead 缓存读证据(读侧方言容忍):usage 对象中任一已知缓存读键非零即证。
// 键集覆盖 openai(cached_tokens)/deepseek(prompt_cache_hit_tokens)/anthropic
// (cache_read_input_tokens)三族;写侧(缓存建立)各方言归一不一,不参与 v1 判定(design 登记)。
func extractCacheRead(raw json.RawMessage) int64 {
	var probe map[string]any
	if err := json.Unmarshal(raw, &probe); err != nil {
		return 0
	}
	// usage 可能平铺也可能嵌套 usage 节:两层内找已知键
	depths := []map[string]any{probe}
	if u, ok := probe["usage"].(map[string]any); ok {
		depths = append(depths, u)
	}
	known := []string{"cached_tokens", "prompt_cache_hit_tokens", "cache_read_input_tokens"}
	for _, layer := range depths {
		for _, key := range known {
			if v, ok := layer[key].(float64); ok && v > 0 {
				return int64(v)
			}
		}
	}
	return 0
}
