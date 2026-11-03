package failover

import (
	"strings"
	"testing"
)

// TestIteratorWithStick_亲和起点(评审 C4 敏感性重构):rand 恒 100 → 无亲和起点恒 pb
// (pa 权重 100 占据 [0,100) 区间);stick=pa 强制起点 pa——把 NewIteratorWithStick
// 突变为 NewIterator 本用例必红。stick=pc 验证低优先级组同样 pin(评审 C9:降级组多成员)。
func TestIteratorWithStick_亲和起点(t *testing.T) {
	cands := []Candidate{
		{Provider: "pa", Priority: 0, Weight: 100},
		{Provider: "pb", Priority: 0, Weight: 1},
		{Provider: "pc", Priority: 1, Weight: 1},
		{Provider: "pd", Priority: 1, Weight: 1},
	}
	rand := func(int) int { return 100 } // 基线加权随机起点恒 pb(两处组内区间核算同此)

	// stick=pa(高优组):起点强制 pa(压制 r=100 的 pb 偏置)
	it := NewIteratorWithStick(cands, rand, "pa")
	c1, _ := it.Next()
	if c1.Provider != "pa" {
		t.Fatalf("stick 起点应 pa: %s", c1.Provider)
	}
	var order []string
	order = append(order, c1.Provider)
	for {
		c, ok := it.Next()
		if !ok {
			break
		}
		order = append(order, c.Provider)
	}
	if strings.Join(order, ",") != "pa,pb,pd,pc" {
		t.Fatalf("环绕覆盖(降级组基线起点 pd): %v", order)
	}

	// stick 不在候选集:逐字节退化为基线加权随机(r=100 → pb)
	it2 := NewIteratorWithStick(cands, rand, "zzz")
	if c, _ := it2.Next(); c.Provider != "pb" {
		t.Fatalf("无 stick 应退化加权随机(r=100→pb): %s", c.Provider)
	}

	// stick=pc(低优先级组):首组走基线(pb),进入降级组时组内起点 pin pc(r=100 基线应 pd)
	it3 := NewIteratorWithStick(cands, rand, "pc")
	first, _ := it3.Next()
	if first.Provider != "pb" {
		t.Fatalf("首组应走基线 pb: %s", first.Provider)
	}
	it3.Next() // pa(首组环绕)
	third, _ := it3.Next()
	if third.Provider != "pc" {
		t.Fatalf("降级组起点应 pin pc(评审 C9): %s", third.Provider)
	}

	// 对照:同参数无 stick 时降级组起点为 pd(证明 pc 断言出自亲和而非巧合)
	it4 := NewIterator(cands, rand)
	it4.Next() // pb
	it4.Next() // pa
	if c, _ := it4.Next(); c.Provider != "pd" {
		t.Fatalf("对照基线降级组起点应 pd: %s", c.Provider)
	}
}
