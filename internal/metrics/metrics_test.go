// 零依赖 metrics 测试：文本格式正确性/禁用零开销语义/标签转义。
package metrics

import (
	"bytes"
	"strings"
	"testing"
)

func TestCounterVec_渲染与禁用(t *testing.T) {
	SetEnabled(true)
	t.Cleanup(func() { SetEnabled(false) })
	c := NewCounterVec("muniu_requests_total", "转发请求计数", "model", "status")
	c.Inc("alias-a", "ok")
	c.Add(2, "alias-a", "error")
	var b bytes.Buffer
	RenderTo(&b)
	out := b.String()
	for _, want := range []string{
		"# HELP muniu_requests_total 转发请求计数",
		"# TYPE muniu_requests_total counter",
		`muniu_requests_total{model="alias-a",status="ok"} 1`,
		`muniu_requests_total{model="alias-a",status="error"} 2`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("输出缺 %q:\n%s", want, out)
		}
	}
	// 禁用后：计数零开销短路，渲染为空。
	SetEnabled(false)
	c.Inc("alias-a", "ok")
	b.Reset()
	RenderTo(&b)
	if b.Len() != 0 {
		t.Fatalf("禁用时渲染应为空: %q", b.String())
	}
}

func TestHistogram_序列格式(t *testing.T) {
	SetEnabled(true)
	t.Cleanup(func() { SetEnabled(false) })
	h := NewHistogram("muniu_request_duration_ms", "请求延迟", []float64{10, 100})
	h.Observe(5)
	h.Observe(50)
	h.Observe(500)
	var b bytes.Buffer
	RenderTo(&b)
	out := b.String()
	for _, want := range []string{
		`muniu_request_duration_ms_bucket{le="10"} 1`,
		`muniu_request_duration_ms_bucket{le="100"} 2`,
		`muniu_request_duration_ms_bucket{le="+Inf"} 3`,
		"muniu_request_duration_ms_sum 555",
		"muniu_request_duration_ms_count 3",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("输出缺 %q:\n%s", want, out)
		}
	}
}

func TestGaugeFunc_标签转义(t *testing.T) {
	SetEnabled(true)
	t.Cleanup(func() { SetEnabled(false) })
	NewGaugeFunc("muniu_keypool_state", "key 池状态", func() []Sample {
		return []Sample{{Labels: [][2]string{{"provider", `a"b\c`}, {"state", "healthy"}}, Value: 2}}
	})
	var b bytes.Buffer
	RenderTo(&b)
	if !strings.Contains(b.String(), `provider="a\"b\\c"`) {
		t.Fatalf("标签值必须转义:\n%s", b.String())
	}
}
