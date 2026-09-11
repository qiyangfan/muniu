// Package metrics 零依赖 Prometheus 文本协议直写(ADR-0009):
// stdlib 实现 counter/gauge/histogram,不引 client 依赖树;采集全部原子操作,不阻塞请求路径。
// 未启用(SetEnabled(false))时仪表不计数、渲染输出空,调用方零开销短路。
package metrics

import (
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

var enabled atomic.Bool

// SetEnabled 全局开关(装配期一次性设定)。
func SetEnabled(v bool) { enabled.Store(v) }

// Enabled 开关状态(调用方据此短路插桩)。
func Enabled() bool { return enabled.Load() }

// Sample 单个时间序列样本(标签为有序 KV)。
type Sample struct {
	Labels [][2]string
	Value  float64
}

// Collector 可渲染的指标收集器。
type Collector interface {
	Meta() (name, help, typ string)
	Collect() []Sample
}

var (
	mu         sync.Mutex
	collectors []Collector
)

func register(c Collector) {
	mu.Lock()
	defer mu.Unlock()
	collectors = append(collectors, c)
}

// RenderTo 以 Prometheus 文本暴露格式(v0.0.4)输出全部已注册收集器。
func RenderTo(w io.Writer) {
	if !Enabled() {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	for _, c := range collectors {
		name, help, typ := c.Meta()
		var b strings.Builder
		b.WriteString("# HELP " + name + " " + help + "\n")
		b.WriteString("# TYPE " + name + " " + typ + "\n")
		if h, ok := c.(*Histogram); ok {
			h.writeTo(&b, name) // histogram 序列带 _bucket/_sum/_count 后缀,单独直写
			_, _ = io.WriteString(w, b.String())
			continue
		}
		samples := c.Collect()
		sort.Slice(samples, func(i, j int) bool { return labelKey(samples[i]) < labelKey(samples[j]) })
		for _, s := range samples {
			b.WriteString(name)
			if len(s.Labels) > 0 {
				b.WriteString("{")
				for i, kv := range s.Labels {
					if i > 0 {
						b.WriteString(",")
					}
					b.WriteString(kv[0] + `="` + escapeLabel(kv[1]) + `"`)
				}
				b.WriteString("}")
			}
			b.WriteString(" " + formatFloat(s.Value) + "\n")
		}
		_, _ = io.WriteString(w, b.String())
	}
}

// CounterVec 带 label 的计数器(动态标签值;禁用时不计数)。
type CounterVec struct {
	name, help string
	labels     []string

	mu     sync.Mutex
	values map[string]*counterEntry // labelKey → 值
}

type counterEntry struct {
	labels []string
	value  atomic.Int64
}

// NewCounterVec 构造并注册。
func NewCounterVec(name, help string, labelNames ...string) *CounterVec {
	c := &CounterVec{name: name, help: help, labels: labelNames, values: map[string]*counterEntry{}}
	register(c)
	return c
}

// Inc 计数 +1(禁用时零开销)。
func (c *CounterVec) Inc(labels ...string) { c.Add(1, labels...) }

// Add 计数 +n(禁用时零开销)。
func (c *CounterVec) Add(n int64, labels ...string) {
	if !Enabled() {
		return
	}
	key := labelKeyOf(labels)
	c.mu.Lock()
	e, ok := c.values[key]
	if !ok {
		e = &counterEntry{labels: append([]string(nil), labels...)}
		c.values[key] = e
	}
	c.mu.Unlock()
	e.value.Add(n)
}

// Meta 实现 Collector。
func (c *CounterVec) Meta() (string, string, string) { return c.name, c.help, "counter" }

// Collect 实现 Collector。
func (c *CounterVec) Collect() []Sample {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Sample, 0, len(c.values))
	for _, e := range c.values {
		pairs := make([][2]string, len(c.labels))
		for i, v := range e.labels {
			pairs[i] = [2]string{c.labels[i], v}
		}
		out = append(out, Sample{Labels: pairs, Value: float64(e.value.Load())})
	}
	return out
}

// GaugeFunc 函数型仪表(抓取时回调取值;用于水位/状态计数等派生值)。
type GaugeFunc struct {
	name, help string
	fn         func() []Sample
}

// NewGaugeFunc 构造并注册。
func NewGaugeFunc(name, help string, fn func() []Sample) *GaugeFunc {
	g := &GaugeFunc{name: name, help: help, fn: fn}
	register(g)
	return g
}

// Meta 实现 Collector。
func (g *GaugeFunc) Meta() (string, string, string) { return g.name, g.help, "gauge" }

// Collect 实现 Collector(禁用时返回空,渲染输出空)。
func (g *GaugeFunc) Collect() []Sample {
	if !Enabled() {
		return nil
	}
	return g.fn()
}

// Histogram 直方图(预定义桶;累计计数与总和)。
type Histogram struct {
	name, help string
	bounds     []float64

	buckets []atomic.Int64
	count   atomic.Int64
	sumBits atomic.Uint64 // float64 位图原子累加
}

// DefBuckets 请求延迟常用桶(ms)。
var DefBuckets = []float64{5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000}

// NewHistogram 构造并注册(桶必须严格升序且 ≤ +Inf;+Inf 序列由渲染层以 count 合成)。
func NewHistogram(name, help string, buckets []float64) *Histogram {
	for i := 1; i < len(buckets); i++ {
		if buckets[i] <= buckets[i-1] {
			panic(fmt.Sprintf("metrics: histogram %s 桶必须严格升序", name))
		}
	}
	h := &Histogram{name: name, help: help, bounds: append([]float64(nil), buckets...)}
	h.buckets = make([]atomic.Int64, len(h.bounds))
	register(h)
	return h
}

// Observe 观测一个值(禁用时零开销)。
func (h *Histogram) Observe(v float64) {
	if !Enabled() {
		return
	}
	for i, b := range h.bounds {
		if v <= b {
			h.buckets[i].Add(1)
		}
	}
	h.count.Add(1)
	for {
		old := h.sumBits.Load()
		sum := math.Float64frombits(old) + v
		if h.sumBits.CompareAndSwap(old, math.Float64bits(sum)) {
			return
		}
	}
}

// Meta 实现 Collector。
func (h *Histogram) Meta() (string, string, string) { return h.name, h.help, "histogram" }

// Collect 实现 Collector(histogram 渲染经 RenderTo 类型分派走 writeTo,此方法不消费)。
func (h *Histogram) Collect() []Sample { return nil }

// writeTo 输出 _bucket(含 +Inf)/_sum/_count 序列。
func (h *Histogram) writeTo(b *strings.Builder, name string) {
	cum := 0.0
	for i, bound := range h.bounds {
		cum = float64(h.buckets[i].Load())
		b.WriteString(name + `_bucket{le="` + formatFloat(bound) + `"} ` + formatFloat(cum) + "\n")
	}
	b.WriteString(name + `_bucket{le="+Inf"} ` + formatFloat(float64(h.count.Load())) + "\n")
	b.WriteString(name + `_sum ` + formatFloat(math.Float64frombits(h.sumBits.Load())) + "\n")
	b.WriteString(name + `_count ` + formatFloat(float64(h.count.Load())) + "\n")
}

func labelKey(s Sample) string {
	parts := make([]string, 0, len(s.Labels))
	for _, kv := range s.Labels {
		parts = append(parts, kv[0]+"="+kv[1])
	}
	return strings.Join(parts, ",")
}

func labelKeyOf(labels []string) string { return strings.Join(labels, "\x00") }

func escapeLabel(v string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return r.Replace(v)
}

// formatFloat 整数值不带小数点,其余走 strconv(合法 Prometheus 浮点)。
func formatFloat(v float64) string {
	if v == math.Trunc(v) && math.Abs(v) < 1e15 {
		return strconv.FormatInt(int64(v), 10)
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}
