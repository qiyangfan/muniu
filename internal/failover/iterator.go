// 候选迭代器：按 priority 升序分组降级，同 priority 组内以加权随机选起点、
// 环绕遍历全组（起点之前的成员仍会被尝试）。随机源可注入，纯函数可穷举测试。
package failover

import "sort"

// Candidate 路由候选，failover 视角的最小形态，proxy 负责从 config 转换。
type Candidate struct {
	Provider string
	Model    string
	Priority int
	Weight   int
}

// Iterator 候选序列：Next 耗尽返回 false。
type Iterator interface {
	Next() (Candidate, bool)
}

// randIntn 随机源：返回 [0,n) 的整数，n<=0 时不会被调用。
type randIntn func(n int) int

type iterator struct {
	groups [][]Candidate // priority 升序的候选组
	rand   randIntn

	gi      int  // 当前组下标
	inGroup bool // 当前组是否已产出
	offset  int  // 组内加权随机起点
	emitted int  // 当前组已产出数
	done    bool
}

// NewIterator 构造：内部复制候选并按 priority 升序稳定分组。
// weight 非法（<=0）按 1 参与起点加权；randIntn 为 nil 时起点恒 0。
func NewIterator(cands []Candidate, randIntn randIntn) Iterator {
	cs := append([]Candidate(nil), cands...)
	sort.SliceStable(cs, func(i, j int) bool {
		return cs[i].Priority < cs[j].Priority
	})
	if randIntn == nil {
		randIntn = func(int) int { return 0 }
	}
	it := &iterator{rand: randIntn}
	for _, c := range cs {
		if n := len(it.groups); n == 0 || it.groups[n-1][0].Priority != c.Priority {
			it.groups = append(it.groups, []Candidate{c})
		} else {
			it.groups[n-1] = append(it.groups[n-1], c)
		}
	}
	return it
}

// Next 产出下一候选：当前组环绕遍历完（含起点之前成员）后进入下一 priority 组。
func (it *iterator) Next() (Candidate, bool) {
	if it.done {
		return Candidate{}, false
	}
	for {
		if it.gi >= len(it.groups) {
			it.done = true
			return Candidate{}, false
		}
		g := it.groups[it.gi]
		if !it.inGroup {
			it.offset = it.startOf(g)
			it.emitted = 0
			it.inGroup = true
		}
		if it.emitted < len(g) {
			c := g[(it.offset+it.emitted)%len(g)]
			it.emitted++
			return c, true
		}
		it.inGroup = false
		it.gi++
	}
}

// startOf 组内加权随机起点：总权重 W 内取 rand(W)，落入累积权重区间的成员即起点。
func (it *iterator) startOf(group []Candidate) int {
	total := 0
	for _, c := range group {
		w := c.Weight
		if w <= 0 {
			w = 1
		}
		total += w
	}
	r := 0
	if total > 0 {
		r = it.rand(total)
		if r < 0 {
			r = 0
		}
		if r >= total {
			r = total - 1
		}
	}
	for i, c := range group {
		w := c.Weight
		if w <= 0 {
			w = 1
		}
		if r < w {
			return i
		}
		r -= w
	}
	return 0
}
