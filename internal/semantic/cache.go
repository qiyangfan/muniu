// Package semantic 提供基于请求指纹和向量相似度的语义缓存。
// 缓存条目受请求与响应准入条件、TTL 和 LRU 容量限制。
package semantic

import (
	"container/list"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/solvela/muniu/internal/store"
)

// Options 缓存参数；零值不可用，Dim/Threshold/Capacity 必须显式配置。
type Options struct {
	Dim            int           // 嵌入维度(1536)
	Threshold      float32       // 相似度阈值，0.97 为保守起步值。
	TTL            time.Duration // 条目 TTL(24h)
	Capacity       int           // LRU 容量上限
	EmbeddingModel string        // embedding 模型别名，经本网关 embeddings 通道调用。
}

// Embedder 嵌入调用通道，由装配层实现，经本网关 /v1/embeddings 调用。
type Embedder interface {
	Embed(ctx context.Context, model, input string) ([]float32, error)
}

// Cache 语义缓存，进程内单例；索引与 LRU 为内存态，DB 为持久层。
type Cache struct {
	opts     Options
	db       *sql.DB
	embedder Embedder
	ix       VectorIndex

	mu      sync.Mutex
	lru     *list.List               // 前端为最近使用，元素为 lruxEntry
	lruKeys map[string]*list.Element // fingerprint → 元素
	hits    int64                    // 累计命中，仅进程内
}

type lruxEntry struct {
	fp     string
	tenant string
	id     string // DB 行 id
}

// Fingerprint 计算归一化指纹：对折叠空白后的指纹文本取 SHA256。
// 指纹文本 = model_alias + \x00 + messages 归一化 JSON + \x00 + tools 指纹。
func Fingerprint(modelAlias, messagesNormalized, toolsFP string) string {
	h := sha256.Sum256([]byte(modelAlias + "\x00" + NormalizeText(messagesNormalized) + "\x00" + toolsFP))
	return hex.EncodeToString(h[:])
}

// NormalizeText 空白抖动折叠：空格、Tab、换行等连续空白折叠为单个空格并去首尾。
func NormalizeText(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// TemperatureAdmissible 温度准入：显式 temperature ≤ 0.3 才可缓存；nil 视为缺省 1.0，不可缓存。
func TemperatureAdmissible(temp *float64) bool {
	return temp != nil && *temp <= 0.3
}

// ResponseAdmissible 响应侧准入：finish_reason=stop、无工具调用、JSON 合法。
// inputTokens 为入站估算 token 数，不超过 8000 才准入。
func ResponseAdmissible(responseBody []byte, inputTokens int) bool {
	if inputTokens > 8000 {
		return false
	}
	var resp struct {
		Choices []struct {
			Message struct {
				ToolCalls []any `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(responseBody, &resp); err != nil {
		return false
	}
	if len(resp.Choices) != 1 || resp.Choices[0].FinishReason != "stop" {
		return false
	}
	return len(resp.Choices[0].Message.ToolCalls) == 0
}

// NewCache 构造并从 DB 加载未过期条目；索引与计数的内存态重启后由此重建。
func NewCache(db *sql.DB, opts Options, embedder Embedder) (*Cache, error) {
	if opts.Dim <= 0 || opts.Capacity <= 0 || opts.TTL <= 0 {
		return nil, fmt.Errorf("semantic: Options 不完整(dim/capacity/ttl 必须显式配置)")
	}
	c := &Cache{
		opts:     opts,
		db:       db,
		embedder: embedder,
		ix:       NewIndex(opts.Dim),
		lru:      list.New(),
		lruKeys:  map[string]*list.Element{},
	}
	rows, err := store.LoadCacheRows(db)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		vec, n := decodeVec(r.Embedding)
		if n != opts.Dim {
			_ = store.DeleteCache(db, r.ID) // 清除维度不符的脏数据
			continue
		}
		c.ixAdd(r.ID, r.TenantID, r.Fingerprint, vec)
	}
	// 启动时清理过期行，尽力而为：失败不阻断启动，下次启动会重试；
	// 读取侧的 expires_at 过滤兜底正确性，此处只影响磁盘回收。
	_, _ = store.DeleteExpiredCache(db, time.Now())
	return c, nil
}

// Options 返回配置的只读副本，调用方不得修改。
func (c *Cache) Options() Options { return c.opts }

// EmbedInput 经嵌入通道计算输入向量；检索与入库共用一次嵌入，免重复调用。
func (c *Cache) EmbedInput(ctx context.Context, input string) ([]float32, error) {
	return c.embedder.Embed(ctx, c.opts.EmbeddingModel, input)
}

// Lookup 两级检索：精确指纹 → 语义 TopK+阈值。命中返回响应体并 hits+1。
func (c *Cache) Lookup(ctx context.Context, tenantID, fingerprint string, vec []float32) (*LookupResult, bool) {
	now := time.Now()
	// 一级：精确指纹。
	if row, err := store.GetCacheByFingerprint(c.db, tenantID, fingerprint, now); err == nil && row != nil {
		return c.hit(row)
	}
	// 二级：语义相似，经向量检索、租户过滤和阈值判断。
	for _, h := range c.ix.TopK(vec, 5) {
		if h.Score < c.opts.Threshold {
			break // 分数降序，后续更低。
		}
		row, err := store.GetCacheByID(c.db, h.ID, now)
		if err != nil || row.TenantID != tenantID {
			continue // 跨租户不命中。
		}
		return c.hit(row)
	}
	return nil, false
}

// hit 命中收尾：hits+1、提升 LRU、返回回放体；含直通双存侧，回放路径由 proxy 按协议与表单择优。
func (c *Cache) hit(row *store.CacheRow) (*LookupResult, bool) {
	_ = store.TouchCacheHits(c.db, row.ID)
	c.mu.Lock()
	c.hits++
	if e, ok := c.lruKeys[row.Fingerprint+"\x00"+row.TenantID]; ok {
		c.lru.MoveToFront(e)
	}
	c.mu.Unlock()
	return &LookupResult{
		Response:       row.Response,
		NativeResponse: row.NativeResponse,
		NativeProto:    row.NativeProto,
		NativeForm:     row.NativeForm,
		Hits:           row.Hits + 1,
	}, true
}

// LookupResult 命中回放体。
type LookupResult struct {
	Response       []byte // 存储的 canonical chat 响应 JSON，非流式直返或流式拼包回放。
	NativeResponse []byte // 直通原始响应体，与 canonical 双存；nil 表示翻译路径条目。
	NativeProto    string // 入站协议标签(messages|responses)
	NativeForm     string // 表单标记(stream|nonstream)
	Hits           int64
}

// Store 写入缓存条目：vec 为调用方在检索阶段已计算的查询向量，一次嵌入两用，免重复调用。
// 写入 = DB upsert + 索引 + LRU；容量满时淘汰最久未使用。
func (c *Cache) Store(_ context.Context, tenantID, fingerprint, modelAlias string, vec []float32, response []byte) error {
	return c.put(tenantID, fingerprint, modelAlias, vec, response, nil, "", "")
}

// StoreNative 直通双存写入：canonical 供跨协议命中，原生体与协议标签、表单标记供同构恒等回放。
func (c *Cache) StoreNative(_ context.Context, tenantID, fingerprint, modelAlias string, vec []float32,
	canonical, native []byte, proto, form string) error {
	return c.put(tenantID, fingerprint, modelAlias, vec, canonical, native, proto, form)
}

// put 落库、索引、LRU；容量满时淘汰最久未使用。
func (c *Cache) put(tenantID, fingerprint, modelAlias string, vec []float32, response, native []byte, proto, form string) error {
	if len(vec) != c.opts.Dim {
		return fmt.Errorf("semantic: 嵌入维度不符: %d ≠ %d", len(vec), c.opts.Dim)
	}
	if err := store.UpsertCacheRowNative(c.db, tenantID, fingerprint, modelAlias, encodeVec(vec),
		response, native, proto, form, c.opts.TTL); err != nil {
		return err
	}
	// 覆盖写：先移除旧条目再写入。
	c.mu.Lock()
	defer c.mu.Unlock()
	key := fingerprint + "\x00" + tenantID
	if e, ok := c.lruKeys[key]; ok {
		old := c.lru.Remove(e).(*lruxEntry)
		c.ix.Remove(old.id)
		delete(c.lruKeys, key)
	}
	row, err := store.GetCacheByFingerprint(c.db, tenantID, fingerprint, time.Now())
	if err != nil {
		return fmt.Errorf("semantic: 回读缓存行失败: %w", err)
	}
	if row == nil {
		// DB 已写而回读缺失，理论上仅并发写覆盖可致；索引不加该行，显式报错。
		return fmt.Errorf("semantic: 回读缓存行缺失(tenant=%s fingerprint=%s)", tenantID, fingerprint)
	}
	c.ixAdd(row.ID, tenantID, fingerprint, vec)
	// 容量淘汰。
	for c.lru.Len() > c.opts.Capacity {
		back := c.lru.Back()
		if back == nil {
			break
		}
		e := back.Value.(*lruxEntry)
		c.lru.Remove(back)
		delete(c.lruKeys, e.fp+"\x00"+e.tenant)
		c.ix.Remove(e.id)
		_ = store.DeleteCache(c.db, e.id)
	}
	return nil
}

// ixAdd 索引与 LRU 双写；调用方须持 c.mu 或处于构造期。
func (c *Cache) ixAdd(id, tenantID, fp string, vec []float32) {
	c.ix.Add(id, vec)
	e := c.lru.PushFront(&lruxEntry{fp: fp, tenant: tenantID, id: id})
	c.lruKeys[fp+"\x00"+tenantID] = e
}

// Stats 管理面统计：条数、累计命中、容量、阈值。
func (c *Cache) Stats() map[string]any {
	s, _ := store.CacheStatsRowGet(c.db)
	c.mu.Lock()
	entries := c.lru.Len()
	hits := c.hits
	c.mu.Unlock()
	if s.Hits > hits {
		hits = s.Hits // 跨重启以 DB 累计为准。
	}
	return map[string]any{
		"entries": entries, "hits": hits,
		"capacity": c.opts.Capacity, "threshold": c.opts.Threshold,
		"embedding_model": c.opts.EmbeddingModel,
	}
}

// encodeVec 与 decodeVec 在向量和 BLOB 间转换，float32 小端字节序。
func encodeVec(vec []float32) []byte {
	out := make([]byte, 4*len(vec))
	for i, f := range vec {
		bits := math.Float32bits(f)
		out[4*i] = byte(bits)
		out[4*i+1] = byte(bits >> 8)
		out[4*i+2] = byte(bits >> 16)
		out[4*i+3] = byte(bits >> 24)
	}
	return out
}

func decodeVec(b []byte) ([]float32, int) {
	if len(b)%4 != 0 {
		return nil, 0
	}
	out := make([]float32, len(b)/4)
	for i := range out {
		bits := uint32(b[4*i]) | uint32(b[4*i+1])<<8 | uint32(b[4*i+2])<<16 | uint32(b[4*i+3])<<24
		out[i] = math.Float32frombits(bits)
	}
	return out, len(out)
}
