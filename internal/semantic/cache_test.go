// 语义缓存测试：指纹归一化、准入白名单、阈值边界、TTL 过期、LRU 容量淘汰、命中计量。
package semantic

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"
)

func TestFingerprint_归一化与区分度(t *testing.T) {
	// 空白抖动归一：多空格、换行、Tab 与单词间单空格同指纹。
	f1 := Fingerprint("alias-a", `{"role":"user","content":"你好,  木牛"}`, "tools")
	f2 := Fingerprint("alias-a", `{"role":"user","content":"你好, 木牛"}`, "tools")
	if f1 != f2 {
		t.Fatalf("空白归一失败:\n%s\n%s", f1, f2)
	}
	// 别名不同 → 指纹不同。
	if Fingerprint("alias-b", `{"a":1}`, "") == Fingerprint("alias-a", `{"a":1}`, "") {
		t.Fatal("不同别名不得同指纹")
	}
	// tools 指纹参与。
	if Fingerprint("alias-a", `{"a":1}`, "t1") == Fingerprint("alias-a", `{"a":1}`, "t2") {
		t.Fatal("不同 tools 不得同指纹")
	}
	if len(f1) != 64 {
		t.Fatalf("指纹应为 sha256 hex(64 字符): %d", len(f1))
	}
}

func TestTemperatureAdmissible_仅低温可缓存(t *testing.T) {
	if !TemperatureAdmissible(ptrFloat(0.0)) || !TemperatureAdmissible(ptrFloat(0.3)) {
		t.Fatal("temperature ≤ 0.3 应可缓存")
	}
	if TemperatureAdmissible(ptrFloat(0.31)) || TemperatureAdmissible(nil) {
		t.Fatal("temperature > 0.3 或未设置(nil=缺省 1.0)不可缓存")
	}
}

func TestAdmission_响应侧白名单(t *testing.T) {
	// finish_reason=stop + 无工具调用 → 准入。
	ok := `{"choices":[{"message":{"role":"assistant","content":"答案"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`
	if !ResponseAdmissible([]byte(ok), 100) {
		t.Fatal("标准 stop 响应应准入")
	}
	// finish_reason=length → 拒。
	length := strings.Replace(ok, `"stop"`, `"length"`, 1)
	if ResponseAdmissible([]byte(length), 100) {
		t.Fatal("length 结束不得准入(答案被截断)")
	}
	// 工具调用 → 拒。
	tool := strings.Replace(ok, `"content":"答案"`, `"tool_calls":[{"id":"x","function":{"name":"f","arguments":"{}"}}]`, 1)
	tool = strings.Replace(tool, `"content":"答案",`, `"content":null,`, 1)
	if ResponseAdmissible([]byte(tool), 100) {
		t.Fatal("工具调用响应不得准入")
	}
	// 输入超 8K tokens → 拒。
	if ResponseAdmissible([]byte(ok), 8001) {
		t.Fatal("输入超 8K tokens 不得准入")
	}
	if !ResponseAdmissible([]byte(ok), 8000) {
		t.Fatal("输入恰 8K 应准入(边界含)")
	}
	// 非法 JSON → 拒。
	if ResponseAdmissible([]byte(`{bad`), 100) {
		t.Fatal("非法响应不得准入")
	}
}

// stubEmbedder 定值嵌入，可控相似度。
type stubEmbedder struct {
	vec   []float32
	err   error
	calls int
}

func (s *stubEmbedder) Embed(ctx context.Context, model, input string) ([]float32, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return s.vec, nil
}

func newTestCache(t *testing.T, embedder Embedder, opts ...func(*Options)) (*Cache, *Options) {
	t.Helper()
	opts0 := &Options{Dim: 4, Threshold: 0.97, TTL: 24 * time.Hour, Capacity: 100, EmbeddingModel: "emb"}
	for _, o := range opts {
		o(opts0)
	}
	c, err := newCacheForTest(t, *opts0, embedder)
	if err != nil {
		t.Fatal(err)
	}
	return c, opts0
}

func TestCache_存储与精确指纹命中(t *testing.T) {
	emb := &stubEmbedder{vec: []float32{1, 0, 0, 0}}
	c, _ := newTestCache(t, emb)

	resp := []byte(`{"choices":[{"message":{"role":"assistant","content":"命中答案"},"finish_reason":"stop"}]}`)
	if err := c.Store(context.Background(), "t1", "fp-exact", "alias-a", []float32{1, 0, 0, 0}, resp); err != nil {
		t.Fatal(err)
	}
	res, hit := c.Lookup(context.Background(), "t1", "fp-exact", []float32{1, 0, 0, 0})
	if !hit {
		t.Fatal("精确指纹应命中")
	}
	if !strings.Contains(string(res.Response), "命中答案") {
		t.Fatalf("回放响应不符: %s", res.Response)
	}
	if res.Hits != 1 {
		t.Fatalf("命中应 hits+1: %d", res.Hits)
	}
}

func TestCache_阈值边界(t *testing.T) {
	emb := &stubEmbedder{vec: []float32{1, 0, 0, 0}}
	c, opts := newTestCache(t, emb)
	if err := c.Store(context.Background(), "t1", "fp", "alias-a", []float32{1, 0, 0, 0},
		[]byte(`{"choices":[{"message":{"content":"a"},"finish_reason":"stop"}]}`)); err != nil {
		t.Fatal(err)
	}

	// 用 0.9701 略高于阈值，余量 1e-4 远大于 float32 计算链的舍入噪声，应命中。
	// 不用精确等值 0.97 构造：相似度浮点在 amd64 与 arm64 舍入路径不同，等值点会算出
	// 0.96999x 而不命中；≥ 语义由 cache.go 的 `Score < Threshold 拒`保证。
	above := cosineVector([]float32{1, 0, 0, 0}, 0.9701)
	res, hit := c.Lookup(context.Background(), "t1", "fp-miss", above)
	if !hit {
		t.Fatal("相似度高于阈值应命中(≥ 语义)")
	}
	_ = res
	// 相似度低于阈值 → 不命中。
	below := cosineVector([]float32{1, 0, 0, 0}, 0.9699)
	if _, hit = c.Lookup(context.Background(), "t1", "fp-miss", below); hit {
		t.Fatal("低于阈值不得命中(宁缺勿错)")
	}
	_ = opts
}

func TestCache_TTL过期(t *testing.T) {
	emb := &stubEmbedder{vec: []float32{1, 0, 0, 0}}
	c, _ := newTestCache(t, emb, func(o *Options) { o.TTL = 50 * time.Millisecond })
	if err := c.Store(context.Background(), "t1", "fp", "alias-a", []float32{1, 0, 0, 0},
		[]byte(`{"choices":[{"message":{"content":"a"},"finish_reason":"stop"}]}`)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Millisecond)
	if _, hit := c.Lookup(context.Background(), "t1", "fp", []float32{1, 0, 0, 0}); hit {
		t.Fatal("TTL 过期不得命中")
	}
}

func TestCache_LRU容量淘汰(t *testing.T) {
	emb := &stubEmbedder{vec: []float32{1, 0, 0, 0}}
	c, _ := newTestCache(t, emb, func(o *Options) { o.Capacity = 2 })
	ctx := context.Background()
	_ = c.Store(ctx, "t1", "fp-1", "a", []float32{1, 0, 0, 0}, []byte(`{"choices":[{"message":{"content":"1"},"finish_reason":"stop"}]}`))
	_ = c.Store(ctx, "t1", "fp-2", "a", []float32{0, 1, 0, 0}, []byte(`{"choices":[{"message":{"content":"2"},"finish_reason":"stop"}]}`))
	// 访问 fp-1 使其成为最近使用。
	if _, hit := c.Lookup(ctx, "t1", "fp-1", []float32{1, 0, 0, 0}); !hit {
		t.Fatal("fp-1 应命中")
	}
	_ = c.Store(ctx, "t1", "fp-3", "a", []float32{0, 0, 1, 0}, []byte(`{"choices":[{"message":{"content":"3"},"finish_reason":"stop"}]}`))
	// 容量 2：最久未使用的 fp-2 被淘汰，fp-1 与 fp-3 保留。
	if _, hit := c.Lookup(ctx, "t1", "fp-2", []float32{0, 1, 0, 0}); hit {
		t.Fatal("容量淘汰:fp-2 应被逐出")
	}
	if _, hit := c.Lookup(ctx, "t1", "fp-1", []float32{1, 0, 0, 0}); !hit {
		t.Fatal("fp-1 应保留")
	}
}

func TestCache_租户隔离(t *testing.T) {
	emb := &stubEmbedder{vec: []float32{1, 0, 0, 0}}
	c, _ := newTestCache(t, emb)
	_ = c.Store(context.Background(), "t1", "fp", "alias-a", []float32{1, 0, 0, 0},
		[]byte(`{"choices":[{"message":{"content":"a"},"finish_reason":"stop"}]}`))
	if _, hit := c.Lookup(context.Background(), "t2", "fp", []float32{1, 0, 0, 0}); hit {
		t.Fatal("跨租户不得命中(non-goals:不做跨租户共享)")
	}
}

func TestStats_命中计数(t *testing.T) {
	emb := &stubEmbedder{vec: []float32{1, 0, 0, 0}}
	c, _ := newTestCache(t, emb)
	_ = c.Store(context.Background(), "t1", "fp", "a", []float32{1, 0, 0, 0}, []byte(`{"choices":[{"message":{"content":"a"},"finish_reason":"stop"}]}`))
	_, _ = c.Lookup(context.Background(), "t1", "fp", []float32{1, 0, 0, 0})
	stats := c.Stats()
	if stats["entries"].(int) != 1 || stats["hits"].(int64) != 1 {
		t.Fatalf("统计不符: %+v", stats)
	}
}

// cosineVector 构造与 base 夹角余弦为 target 的单位向量，平面旋转法，测试阈值边界用。
func cosineVector(base []float32, target float64) []float32 {
	var norm float64
	for _, x := range base {
		norm += float64(x) * float64(x)
	}
	norm = math.Sqrt(norm)
	u := make([]float64, len(base))
	for i, x := range base {
		u[i] = float64(x) / norm
	}
	orth := make([]float64, len(base))
	orth[0], orth[1] = -u[1], u[0]
	var onorm float64
	for _, x := range orth {
		onorm += x * x
	}
	onorm = math.Sqrt(onorm)
	sin := math.Sqrt(1 - target*target)
	out := make([]float32, len(base))
	for i := range out {
		out[i] = float32(target*u[i] + sin*orth[i]/onorm)
	}
	return out
}
