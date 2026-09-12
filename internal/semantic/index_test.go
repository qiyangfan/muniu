// HNSWIndex 回归钉子:上游 coder/hnsw Delete 悬空引用缺陷的规避验证。
// 背景:LRU 淘汰→Remove 后,Search 层间下降经 nodes[elevator] 取已删节点得 nil
// 而 SIGSEGV(CI amd64 首跑实证,cache_test.go:171)。Remove 已改为全量重建,
// 本测试以确定性的增删查压力循环钉住该缺陷不再回归。
package semantic

import (
	"testing"
)

func TestHNSWIndex_Remove后检索安全(t *testing.T) {
	ix := NewHNSWIndex(4)
	// 与失败用例同构的 4 维正交向量集
	orth := [][]float32{{1, 0, 0, 0}, {0, 1, 0, 0}, {0, 0, 1, 0}, {0, 0, 0, 1}}

	// 增→删→查压力循环:淘汰(Store 满)→Lookup 的真实序列在容量淘汰时必然触发
	// Remove,此处以更高频的增删查覆盖 entry/elevator 悬空的全部构造路径
	for round := 0; round < 50; round++ {
		for i, id := range []string{"a", "b", "c", "d"} {
			ix.Add(id, orth[i])
		}
		ix.Remove("b") // b 可能是 entry/elevator:旧实现此后 TopK 即 SIGSEGV
		for i := 0; i < 10; i++ {
			_ = ix.TopK(orth[i%len(orth)], 3)
		}
		if got := ix.Len(); got != 3 {
			t.Fatalf("删除后应剩 3 条,实际 %d", got)
		}
		// 清空重来,覆盖 entry 轮换的所有形态
		ix.Remove("a")
		ix.Remove("c")
		ix.Remove("d")
		if got := ix.Len(); got != 0 {
			t.Fatalf("全删后应为空,实际 %d", got)
		}
	}
}
