//go:build eval

// 本基准对比 1 万条 1536 维向量的暴力搜索与 HNSW 延迟。
package semantic

import (
	"fmt"
	"math/rand"
	"sort"
	"testing"
	"time"
)

func genVectors(n, dim int) [][]float32 {
	rng := rand.New(rand.NewSource(42))
	out := make([][]float32, n)
	for i := range out {
		v := make([]float32, dim)
		for j := range v {
			v[j] = rng.Float32()
		}
		out[i] = v
	}
	return out
}

// TestIndexEval_1万条1536维对比 输出两种索引的延迟分位数。
func TestIndexEval_1万条1536维对比(t *testing.T) {
	const n, dim, k, queries = 10_000, 1536, 5, 100
	vecs := genVectors(n, dim)

	eval := func(name string, ix VectorIndex) {
		for i, v := range vecs {
			ix.Add(fmt.Sprintf("id-%d", i), v)
		}
		lat := make([]float64, 0, queries)
		for i := 0; i < queries; i++ {
			qv := vecs[(i*137)%n]
			start := time.Now()
			hits := ix.TopK(qv, k)
			lat = append(lat, float64(time.Since(start).Microseconds())/1000.0)
			if len(hits) != k {
				t.Fatalf("%s TopK 数量不符: %d", name, len(hits))
			}
		}
		sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
		t.Logf("%s:p50=%.2fms p99=%.2fms", name, lat[len(lat)*50/100], lat[len(lat)*99/100])
	}

	eval("暴力余弦", NewBruteIndex(dim))
	eval("HNSW", NewHNSWIndex(dim))
}

// BenchmarkBruteCosineTop5 与 BenchmarkHNSWTop5 基准形态。
func BenchmarkBruteCosineTop5(b *testing.B) {
	const n, dim, k = 10_000, 1536, 5
	vecs := genVectors(n, dim)
	ix := NewBruteIndex(dim)
	for i, v := range vecs {
		ix.Add(fmt.Sprintf("id-%d", i), v)
	}
	q := vecs[0]
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ix.TopK(q, k)
	}
}

func BenchmarkHNSWTop5(b *testing.B) {
	const n, dim, k = 10_000, 1536, 5
	vecs := genVectors(n, dim)
	ix := NewHNSWIndex(dim)
	for i, v := range vecs {
		ix.Add(fmt.Sprintf("id-%d", i), v)
	}
	q := vecs[0]
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ix.TopK(q, k)
	}
}
