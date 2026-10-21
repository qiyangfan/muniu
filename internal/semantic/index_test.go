// 验证重复增删查后，删除的 HNSW 节点不会造成悬空引用。
package semantic

import (
	"testing"
)

func TestHNSWIndex_Remove后检索安全(t *testing.T) {
	ix := NewHNSWIndex(4)
	// 使用 4 维正交向量覆盖不同的检索方向。
	orth := [][]float32{{1, 0, 0, 0}, {0, 1, 0, 0}, {0, 0, 1, 0}, {0, 0, 0, 1}}

	// 高频增删查覆盖 entry 和 elevator 节点被移除的路径。
	for round := 0; round < 50; round++ {
		for i, id := range []string{"a", "b", "c", "d"} {
			ix.Add(id, orth[i])
		}
		ix.Remove("b")
		for i := 0; i < 10; i++ {
			_ = ix.TopK(orth[i%len(orth)], 3)
		}
		if got := ix.Len(); got != 3 {
			t.Fatalf("删除后应剩 3 条,实际 %d", got)
		}
		// 清空索引后重试，覆盖 entry 节点轮换后的删除路径。
		ix.Remove("a")
		ix.Remove("c")
		ix.Remove("d")
		if got := ix.Len(); got != 0 {
			t.Fatalf("全删后应为空,实际 %d", got)
		}
	}
}
