// fake 上游是可编程 httptest.Server：流式剧本由事件序列声明，支持慢帧、半帧、中途断。
// fake 也是代码，剧本自身必须有测试；本包仅供测试与 smoke 构建消费。

package testkit

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// Event 流式剧本的单个事件。
type Event struct {
	Data        string        // 事件完整线上字节，多行 data 合法，应以 \n\n 结尾。
	DelayBefore time.Duration // 发送前等待，模拟帧间间隔与首帧迟滞；客户端断连则提前返回。
	SplitAt     int           // 大于 0 时按该字节偏移切片发送，隔一拍再发余下，模拟半帧。
	CloseAfter  bool          // 发送后劫持连接直接掐断，不发 [DONE]，模拟中途断流。
}

// Upstream fake 上游句柄，提供 URL 与请求记录，供 adapter/proxy 测试断言出站形态。
type Upstream struct {
	server *httptest.Server

	mu      sync.Mutex
	bodies  [][]byte
	headers []http.Header
}

// URL 返回 fake 上游基址。
func (u *Upstream) URL() string { return u.server.URL }

// Close 手动关闭；Cleanup 已自动注册，一般无需调用。
func (u *Upstream) Close() { u.server.Close() }

// RequestCount 收到的请求数。
func (u *Upstream) RequestCount() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.bodies)
}

// LastBody 最近一次请求 body 原始字节。
func (u *Upstream) LastBody() []byte {
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.bodies) == 0 {
		return nil
	}
	return u.bodies[len(u.bodies)-1]
}

// LastHeader 最近一次请求的指定头。
func (u *Upstream) LastHeader(key string) string {
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.headers) == 0 {
		return ""
	}
	return u.headers[len(u.headers)-1].Get(key)
}

// StartStreamUpstream 启动流式 fake，任何请求都按剧本吐 SSE。
func StartStreamUpstream(tb testing.TB, events []Event) *Upstream {
	tb.Helper()
	u := &Upstream{}
	u.server = httptest.NewServer(u.serveStream(events))
	tb.Cleanup(u.server.Close)
	return u
}

// StartJSONUpstream 启动非流式 fake，任何请求都返回固定状态与 body。
func StartJSONUpstream(tb testing.TB, status int, body string) *Upstream {
	tb.Helper()
	u := &Upstream{}
	u.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.record(r)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	tb.Cleanup(u.server.Close)
	return u
}

// record 记录请求，body 先读全，记录失败不干扰剧本。
func (u *Upstream) record(r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	u.recordBody(r, body)
}

// recordBody 以调用方已读的 body 记录请求，供 body 需二次消费的替身使用。
func (u *Upstream) recordBody(r *http.Request, body []byte) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.bodies = append(u.bodies, body)
	u.headers = append(u.headers, r.Header.Clone())
}

// serveStream 剧本执行器：逐事件延时、可选半帧切分、写出并 flush；CloseAfter 用
// Hijack 直接掐 TCP，让客户端读到意外 EOF，与优雅关流严格区分。
func (u *Upstream) serveStream(events []Event) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u.record(r)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		ctrl := http.NewResponseController(w)
		_ = ctrl.Flush()

		for _, ev := range events {
			if ev.DelayBefore > 0 {
				select {
				case <-time.After(ev.DelayBefore):
				case <-r.Context().Done():
					return
				}
			}
			data := ev.Data
			if ev.SplitAt > 0 && ev.SplitAt < len(data) {
				if _, err := w.Write([]byte(data[:ev.SplitAt])); err != nil {
					return
				}
				_ = ctrl.Flush()
				select {
				case <-time.After(50 * time.Millisecond):
				case <-r.Context().Done():
					return
				}
				data = data[ev.SplitAt:]
			}
			if _, err := w.Write([]byte(data)); err != nil {
				return
			}
			_ = ctrl.Flush()
			if ev.CloseAfter {
				conn, _, err := ctrl.Hijack()
				if err == nil {
					_ = conn.Close()
				}
				return
			}
		}
	}
}

// ---- 内置剧本：payload 均为 OpenAI 兼容形态 ----

// chunkData 把 JSON payload 包成完整 SSE data 事件，带 data: 前缀与空行结尾。
func chunkData(payload string) string { return "data: " + payload + "\n\n" }

// OpenAI 形态 chunk 模板：role 帧、content 增量、usage 终帧。
const (
	roleChunk    = `{"id":"chatcmpl-1","choices":[{"index":0,"delta":{"role":"assistant"}}]}`
	contentChunk = `{"id":"chatcmpl-1","choices":[{"index":0,"delta":{"content":"%s"}}]}`
	usageChunk   = `{"id":"chatcmpl-1","choices":[],"usage":{"prompt_tokens":12,"completion_tokens":34}}`
	doneEvent    = "data: [DONE]\n\n"
)

// NormalEvents 正常流：role 帧 → 两段 content → usage 终帧 → [DONE]。
func NormalEvents() []Event {
	return []Event{
		{Data: chunkData(roleChunk)},
		{Data: chunkData(`{"id":"chatcmpl-1","choices":[{"index":0,"delta":{"content":"你好"}}]}`)},
		{Data: chunkData(`{"id":"chatcmpl-1","choices":[{"index":0,"delta":{"content":",世界"}}]}`)},
		{Data: chunkData(usageChunk)},
		{Data: doneEvent},
	}
}

// SlowChunkEvents 慢帧流：每帧前等待 d，用于触发帧间超时剧本；d 传小值即成正常流。
func SlowChunkEvents(d time.Duration) []Event {
	events := NormalEvents()
	for i := range events {
		events[i].DelayBefore = d
	}
	return events
}

// MidStreamCutEvents 中途断流：一段 content 后直接掐断，无 usage、无 [DONE]。
func MidStreamCutEvents() []Event {
	return []Event{
		{Data: chunkData(roleChunk)},
		{Data: chunkData(`{"id":"chatcmpl-1","choices":[{"index":0,"delta":{"content":"部分"}}]}`), CloseAfter: true},
	}
}

// HalfFrameEvents 半帧：第二个事件的字节在 SplitAt 处被切开发送。
func HalfFrameEvents() []Event {
	split := chunkData(`{"id":"chatcmpl-1","choices":[{"index":0,"delta":{"content":"被切开的一帧"}}]}`)
	return []Event{
		{Data: chunkData(roleChunk)},
		{Data: split, SplitAt: len("data: {\"id\":\"chatcmpl-1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"被切")},
		{Data: chunkData(usageChunk)},
		{Data: doneEvent},
	}
}

// UsageMissingEvents usage 缺失流：有内容与 [DONE] 但无 usage 终帧，触发估算路径。
func UsageMissingEvents() []Event {
	return []Event{
		{Data: chunkData(roleChunk)},
		{Data: chunkData(`{"id":"chatcmpl-1","choices":[{"index":0,"delta":{"content":"没有usage"}}]}`)},
		{Data: doneEvent},
	}
}

// ---- 非流式剧本 ----

// JSONOKBody 合法非流式 OpenAI 响应。
const JSONOKBody = `{"id":"chatcmpl-2","object":"chat.completion","model":"fake","choices":[{"index":0,"message":{"role":"assistant","content":"完整回答"},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":9}}`

// JSON500Body 上游 5xx 错误响应。
const JSON500Body = `{"error":{"message":"upstream exploded","type":"server_error"}}`

// ---- 有状态 Responses 替身 ----

// responsesUpstream 在内存中保存响应，提供创建和查询接口；数据形态遵循 OpenAI Responses。
type responsesUpstream struct {
	*Upstream
	mu     sync.Mutex
	stored map[string]string // respID → 存储体
	next   int
}

// Stored 查询内存存储，供断言链式上下文与 GET 命中使用。
func (u *responsesUpstream) Stored(id string) (string, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	body, ok := u.stored[id]
	return body, ok
}

// StartResponsesUpstream 启动有状态 fake：POST /responses 创建并分配 resp_N，
// GET /responses/{id} 返回存储体，未创建返回 404。
func StartResponsesUpstream(tb testing.TB) *responsesUpstream {
	tb.Helper()
	base := &Upstream{}
	u := &responsesUpstream{Upstream: base, stored: map[string]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /responses", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		base.recordBody(r, body)
		u.mu.Lock()
		u.next++
		id := "resp_" + itoa(u.next)
		u.stored[id] = string(body)
		u.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"` + id + `","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"已收到"}]}],"usage":{"input_tokens":13,"output_tokens":1}}`))
	})
	mux.HandleFunc("/responses/{id}", func(w http.ResponseWriter, r *http.Request) {
		base.record(r)
		id := r.PathValue("id")
		if r.Method == http.MethodDelete {
			// 状态 lane 的删除动词：删除存储并回 OpenAI 删除契约形态，存在性校验交调用方。
			u.mu.Lock()
			_, ok := u.stored[id]
			delete(u.stored, id)
			u.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":{"message":"No response found with id ` + id + `."}}`))
				return
			}
			_, _ = w.Write([]byte(`{"id":"` + id + `","deleted":true}`))
			return
		}
		u.mu.Lock()
		body, ok := u.stored[id]
		u.mu.Unlock()
		if !ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"message":"No response found with id ` + id + `."}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})
	base.server = httptest.NewServer(mux)
	tb.Cleanup(base.server.Close)
	return u
}

// itoa 极小整数转字符串，testkit 内零依赖。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	pos := len(b)
	for n > 0 {
		pos--
		b[pos] = byte('0' + n%10)
		n /= 10
	}
	return string(b[pos:])
}
