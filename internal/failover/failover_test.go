// failover 契约测试(测试先行域):判定表逐格表驱动,一格一用例、用例名含格坐标;
// 候选迭代器:同 priority 加权随机起点、priority 递增降级、耗尽即停。
package failover

import "testing"

// TestDecide_判定表 proxy-and-streaming「Failover 判定表」全格覆盖。
// 用例名 = 行(错误分类)× 列(未过/已过首 token)× 流式与否。
func TestDecide_判定表(t *testing.T) {
	cases := []struct {
		coord          string // 格坐标:分类 × 首token × 流式
		class          ErrorClass
		pastFirstToken bool
		stream         bool
		want           Action
	}{
		// dial_fail 行
		{"dial_fail×未过×流式", ClassDialFail, false, true, ActionNextCandidate},
		{"dial_fail×已过×流式(防御格,定义上不可出现)", ClassDialFail, true, true, ActionAbort},
		{"dial_fail×非流式(无首token态)", ClassDialFail, true, false, ActionNextCandidate},
		// timeout_first 行
		{"timeout_first×未过×流式", ClassTimeoutFirst, false, true, ActionNextCandidate},
		{"timeout_first×已过×流式(防御格)", ClassTimeoutFirst, true, true, ActionAbort},
		{"timeout_first×非流式", ClassTimeoutFirst, false, false, ActionNextCandidate},
		// upstream_5xx 行
		{"upstream_5xx×未过×流式", ClassUpstream5xx, false, true, ActionNextCandidate},
		{"upstream_5xx×已过×流式→断言", ClassUpstream5xx, true, true, ActionAbort},
		{"upstream_5xx×非流式", ClassUpstream5xx, false, false, ActionNextCandidate},
		// rate_limited_429 行
		{"rate_limited_429×未过×流式", ClassRateLimited, false, true, ActionNextCandidate},
		{"rate_limited_429×已过×流式→断言", ClassRateLimited, true, true, ActionAbort},
		{"rate_limited_429×非流式", ClassRateLimited, false, false, ActionNextCandidate},
		// auth_failed 行
		{"auth_failed×未过×流式", ClassAuthFailed, false, true, ActionNextCandidate},
		{"auth_failed×已过×流式→断言", ClassAuthFailed, true, true, ActionAbort},
		{"auth_failed×非流式", ClassAuthFailed, false, false, ActionNextCandidate},
		// bad_request_400 行(唯一「不换道直接返回」)
		{"bad_request_400×未过×流式→直接返回", ClassBadRequest, false, true, ActionReturn},
		{"bad_request_400×已过×流式→断言", ClassBadRequest, true, true, ActionAbort},
		{"bad_request_400×非流式→直接返回", ClassBadRequest, false, false, ActionReturn},
		// mid_stream_fail 行(定义上已过首 token;未过=首 token 前断流,零输出可重放,补充格)
		{"mid_stream_fail×未过×流式(补充格:首token前断流可重放)", ClassMidStreamFail, false, true, ActionNextCandidate},
		{"mid_stream_fail×已过×流式→断言", ClassMidStreamFail, true, true, ActionAbort},
		{"mid_stream_fail×非流式(响应中断,可重放)", ClassMidStreamFail, false, false, ActionNextCandidate},
		// context_canceled 行(唯一静默)
		{"context_canceled×未过×流式→静默", ClassContextCanceled, false, true, ActionSilent},
		{"context_canceled×已过×流式→静默(计量已发生部分)", ClassContextCanceled, true, true, ActionSilent},
		{"context_canceled×非流式→静默", ClassContextCanceled, false, false, ActionSilent},
		// unknown(表外防御:按 5xx 同待遇)
		{"unknown×未过×流式", ClassUnknown, false, true, ActionNextCandidate},
		{"unknown×已过×流式", ClassUnknown, true, true, ActionAbort},
		// cost_budget_exceeded 行(网关策略类:静默终结不换道,不计 key 失败——context_canceled 同族)
		{"cost_budget_exceeded×未过×流式→静默", ClassCostBudget, false, true, ActionSilent},
		{"cost_budget_exceeded×已过×流式→静默", ClassCostBudget, true, true, ActionSilent},
		{"cost_budget_exceeded×非流式→静默", ClassCostBudget, false, false, ActionSilent},
	}
	for _, c := range cases {
		t.Run(c.coord, func(t *testing.T) {
			got := Decide(c.class, c.pastFirstToken, c.stream)
			if got != c.want {
				t.Fatalf("Decide(%s, past=%v, stream=%v) = %s, 期望 %s",
					c.class, c.pastFirstToken, c.stream, got, c.want)
			}
		})
	}
}

func TestIterator_加权随机起点与priority降级(t *testing.T) {
	// 候选:p0 组 {A w1, B w3}(总 4),p1 组 {C w1},p2 组 {D w1}
	cands := []Candidate{
		{Provider: "p", Model: "C", Priority: 1, Weight: 1},
		{Provider: "p", Model: "A", Priority: 0, Weight: 1},
		{Provider: "p", Model: "D", Priority: 2, Weight: 1},
		{Provider: "p", Model: "B", Priority: 0, Weight: 3},
	}

	// randInt(4) 恒返 3 → p0 组起点=index 3 的模位置:加权和 [A(0..0), B(1..3)] → 3 落 B
	// 序列应:B, A(环绕), C(p1 降级), D(p2 降级)
	it := NewIterator(cands, func(n int) int { return n - 1 })
	var seq []string
	for {
		c, ok := it.Next()
		if !ok {
			break
		}
		seq = append(seq, c.Model)
	}
	if joined := joinModels(seq); joined != "B,A,C,D" {
		t.Fatalf("起点加权+降级序列不符: %s", joined)
	}

	// randInt 恒返 0 → 起点=A:序列 A, B, C, D
	it2 := NewIterator(cands, func(n int) int { return 0 })
	seq2 := nil2slice(it2)
	if got := joinModels(models(seq2)); got != "A,B,C,D" {
		t.Fatalf("起点=A 序列不符: %s", got)
	}
}

func TestIterator_单候选与耗尽(t *testing.T) {
	it := NewIterator([]Candidate{{Provider: "p", Model: "X", Priority: 0, Weight: 1}},
		func(n int) int { return 0 })
	c, ok := it.Next()
	if !ok || c.Model != "X" {
		t.Fatalf("单候选首次应命中: %v %v", c, ok)
	}
	if _, ok := it.Next(); ok {
		t.Fatal("第二次应耗尽")
	}
	// 空候选
	empty := NewIterator(nil, func(n int) int { return 0 })
	if _, ok := empty.Next(); ok {
		t.Fatal("空候选应直接耗尽")
	}
}

func TestCandidate零权重按1参与(t *testing.T) {
	cands := []Candidate{
		{Provider: "p", Model: "A", Priority: 0, Weight: 0}, // 非法权重按 1
		{Provider: "p", Model: "B", Priority: 0, Weight: 1},
	}
	// randInt(2) 恒 1 → 起点 B:B, A
	it := NewIterator(cands, func(n int) int { return 1 })
	var seq []string
	for {
		c, ok := it.Next()
		if !ok {
			break
		}
		seq = append(seq, c.Model)
	}
	if joinModels(seq) != "B,A" {
		t.Fatalf("零权重按 1 处理后序列不符: %s", joinModels(seq))
	}
}

// models 候选切片 → 模型名切片。
func models(cs []Candidate) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.Model)
	}
	return out
}

// joinModels 逗号连接(防 nil 切片)。
func joinModels(m []string) string {
	s := ""
	for i, v := range m {
		if i > 0 {
			s += ","
		}
		s += v
	}
	return s
}

func nil2slice(it Iterator) []Candidate {
	var out []Candidate
	for {
		c, ok := it.Next()
		if !ok {
			return out
		}
		out = append(out, c)
	}
}
