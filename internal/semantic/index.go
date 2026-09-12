// 语义缓存向量索引(T4.4 选型评估结论):
// 评估(1 万条×1536 维 Top5,本机 2026-09-10):暴力余弦 p99≈21ms,超 5ms 判据 →
// **按契约引入纯 Go HNSW(github.com/coder/hnsw,唯一向量索引依赖)**。
// 两个实现共用 VectorIndex 接口:暴力版保留作对照与小规模回退;生产默认 HNSW。
// HNSW 为近似检索:TopK 召回非精确,经阈值 0.97 过滤后满足缓存「宁缺勿错」要求。
package semantic

import (
	"math"
	"sort"
	"sync"

	hnsw "github.com/coder/hnsw"
)

// VectorIndex 向量索引接口。
type VectorIndex interface {
	Add(id string, vec []float32)
	Remove(id string)
	Len() int
	TopK(query []float32, k int) []Hit
}

// Hit 检索命中:条目标识与余弦相似度(归一化向量下即点积,[-1,1])。
type Hit struct {
	ID    string
	Score float32
}

// normalize 向量 L2 归一化(零向量原样返回)。
func normalize(v []float32) []float32 {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if sum == 0 {
		return v
	}
	inv := float32(1 / math.Sqrt(sum))
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = x * inv
	}
	return out
}

// NewIndex 生产默认索引:HNSW(评估结论)。
func NewIndex(dim int) VectorIndex {
	return NewHNSWIndex(dim)
}

// HNSWIndex coder/hnsw 封装:自带读写锁(coder/hnsw Graph 非并发安全)。
type HNSWIndex struct {
	mu sync.RWMutex
	g  *hnsw.Graph[string]
	// vectors 存活条目(id→归一化向量)。上游 Graph.Delete 留悬空 entry/elevator
	// 引用(v0.6.1 Search 层间下降 nodes[elevator] 对已删节点得 nil → SIGSEGV),
	// 故删除走墓碑:图只增不删,TopK 过滤死节点;墓碑过半才全量重建(均摊 O(1))。
	vectors map[string][]float32
	dead    map[string]struct{}
}

// NewHNSWIndex 构造(dim 仅作维度记录,HNSW 自按向量维度断言)。
func NewHNSWIndex(dim int) *HNSWIndex {
	g := hnsw.NewGraph[string]()
	g.Distance = hnsw.CosineDistance
	return &HNSWIndex{g: g, vectors: make(map[string][]float32), dead: make(map[string]struct{})}
}

// Add 追加条目(重复存活 id 幂等忽略;命中墓碑 id 则重建后插,见图结构一致性)。
func (ix *HNSWIndex) Add(id string, vec []float32) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if _, dup := ix.vectors[id]; dup {
		return
	}
	if _, tomb := ix.dead[id]; tomb {
		delete(ix.dead, id)
		ix.rebuildLocked()
	}
	nv := normalize(vec)
	ix.vectors[id] = nv
	ix.g.Add(hnsw.MakeNode(id, nv))
}

// Remove 删除条目(墓碑标记:规避上游 Delete 悬空引用缺陷,见 vectors 字段注释)。
func (ix *HNSWIndex) Remove(id string) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if _, ok := ix.vectors[id]; !ok {
		return
	}
	delete(ix.vectors, id)
	ix.dead[id] = struct{}{}
	// 墓碑过半才重建:重图结构并清墓碑,保证死节点不随累积劣化召回
	if len(ix.dead) > len(ix.vectors) {
		ix.rebuildLocked()
	}
}

// rebuildLocked 全量重建(调用方持写锁)。
func (ix *HNSWIndex) rebuildLocked() {
	ix.g = hnsw.NewGraph[string]()
	ix.g.Distance = hnsw.CosineDistance
	ix.dead = make(map[string]struct{})
	for id, v := range ix.vectors {
		ix.g.Add(hnsw.MakeNode(id, v))
	}
}

// Len 条数。
func (ix *HNSWIndex) Len() int {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return len(ix.vectors)
}

// TopK 近似余弦 Top-K(分数降序)。墓碑节点过滤:死节点可能占名额致召回略少,
// 语义缓存「宁缺勿错」容忍;墓碑过半已在 Remove 时重建兜底。
func (ix *HNSWIndex) TopK(query []float32, k int) []Hit {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	q := normalize(query)
	nodes := ix.g.Search(q, k)
	hits := make([]Hit, 0, len(nodes))
	for _, n := range nodes {
		if _, alive := ix.vectors[n.Key]; !alive {
			continue
		}
		// coder/hnsw 距离=1-余弦相似度,还原为相似度
		hits = append(hits, Hit{ID: n.Key, Score: 1 - ix.g.Distance(n.Value, q)})
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	return hits
}

// BruteIndex 暴力余弦索引:零依赖对照实现(评估基线/小规模回退)。
type BruteIndex struct {
	mu   sync.RWMutex
	dim  int
	ids  []string
	vecs [][]float32
}

// NewBruteIndex 构造。
func NewBruteIndex(dim int) *BruteIndex { return &BruteIndex{dim: dim} }

// Len 条数。
func (ix *BruteIndex) Len() int {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return len(ix.ids)
}

// Add 追加条目。
func (ix *BruteIndex) Add(id string, vec []float32) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	ix.ids = append(ix.ids, id)
	ix.vecs = append(ix.vecs, normalize(vec))
}

// Remove 按 id 删除。
func (ix *BruteIndex) Remove(id string) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	for i, id2 := range ix.ids {
		if id2 == id {
			ix.ids = append(ix.ids[:i], ix.ids[i+1:]...)
			ix.vecs = append(ix.vecs[:i], ix.vecs[i+1:]...)
			return
		}
	}
}

// TopK 精确余弦 Top-K(分数降序)。
func (ix *BruteIndex) TopK(query []float32, k int) []Hit {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	if len(query) != ix.dim || len(ix.ids) == 0 {
		return nil
	}
	q := normalize(query)
	hits := make([]Hit, 0, len(ix.ids))
	for i, v := range ix.vecs {
		var dot float32
		for j := range v {
			dot += v[j] * q[j]
		}
		hits = append(hits, Hit{ID: ix.ids[i], Score: dot})
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	if k < len(hits) {
		hits = hits[:k]
	}
	return hits
}
