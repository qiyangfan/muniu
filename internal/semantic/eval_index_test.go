//go:build eval

// 索引选型评估(T4.4)的暴力余弦对照实现,仅评估/基准构建使用;
// 生产代码只走 HNSWIndex,故不进常规构建。
package semantic

import (
	"sort"
	"sync"
)

// BruteIndex 暴力余弦索引,零依赖对照实现,作评估基线。
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

// TopK 精确余弦 Top-K,分数降序。
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
