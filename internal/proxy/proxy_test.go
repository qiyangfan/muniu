// proxy 契约测试:fake 剧本 × 客户端可见字节序列逐字节等价(passthrough 契约)、
// 超时/断流错误信封形态、断连传导。半帧用例名直接证明「不解析半事件」。
package proxy

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/solvela/muniu/internal/adapter"
	"github.com/solvela/muniu/internal/config"
	"github.com/solvela/muniu/internal/keypool"
	"github.com/solvela/muniu/internal/protocol"
	"github.com/solvela/muniu/internal/testkit"
)

// newTestProxy 组装指向 fake 上游的单候选代理,返回可请求的 httptest.Server。
func newTestProxy(t *testing.T, upstreamURL string, timeouts Timeouts) *httptest.Server {
	t.Helper()
	reg := adapter.NewRegistry()
	reg.Register("fake", adapter.NewOpenAI(upstreamURL, "sk-fake"))
	p := New(Config{
		Registry: reg,
		Routes: map[string][]config.Candidate{
			"alias-a": {{Provider: "fake", Model: "upstream-a", Priority: 0}},
		},
		Timeouts: timeouts,
		Pools: map[string]*keypool.Pool{
			"fake": keypool.NewPool([]keypool.KeyInput{{ID: "K", Value: "sk-test", Weight: 1}}),
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ts := httptest.NewServer(p.Handler())
	t.Cleanup(ts.Close)
	return ts
}

// postChat 发一条 /v1/chat/completions 请求,返回响应与 body 全文。
func postChat(t *testing.T, url, body string) (*http.Response, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", url+"/v1/chat/completions", strings.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读响应失败: %v", err)
	}
	_ = resp.Body.Close()
	return resp, string(b)
}

const chatBody = `{"model":"alias-a","messages":[{"role":"user","content":"hi"}]}`

// streamChatBody 流式请求(显式 stream:true);所有 SSE 用例必须用它,否则走非流式路径测不到透传契约。
const streamChatBody = `{"model":"alias-a","stream":true,"messages":[{"role":"user","content":"hi"}]}`

// joinEvents 剧本期望字节 = 事件依次拼接。
func joinEvents(events []testkit.Event) string {
	var sb strings.Builder
	for _, ev := range events {
		sb.WriteString(ev.Data)
	}
	return sb.String()
}

func TestStream_normal_客户端字节逐字节等价(t *testing.T) {
	events := testkit.NormalEvents()
	up := testkit.StartStreamUpstream(t, events)
	ts := newTestProxy(t, up.URL(), defTimeouts())

	resp, body := postChat(t, ts.URL, streamChatBody)
	if resp.StatusCode != 200 {
		t.Fatalf("状态码 %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("流式响应头不符: %q", got)
	}
	if body != joinEvents(events) {
		t.Fatalf("字节不等价:\n got=%q\nwant=%q", body, joinEvents(events))
	}
}

func TestStream_half_frame_不解析半事件_字节逐字节等价(t *testing.T) {
	// 用例名即证明:半帧剧本下客户端必须收到与剧本完全一致的完整事件序列,
	// 半帧只能缓冲等待补齐,绝不产出半个事件。
	events := testkit.HalfFrameEvents()
	up := testkit.StartStreamUpstream(t, events)
	ts := newTestProxy(t, up.URL(), defTimeouts())

	_, body := postChat(t, ts.URL, streamChatBody)
	if body != joinEvents(events) {
		t.Fatalf("半帧被错误处理(字节不等价):\n got=%q\nwant=%q", body, joinEvents(events))
	}
	if !strings.Contains(body, `"content":"被切开的一帧"`) {
		t.Fatalf("被切开的事件不完整: %q", body)
	}
}

func TestStream_usage_missing_字节逐字节等价(t *testing.T) {
	events := testkit.UsageMissingEvents()
	up := testkit.StartStreamUpstream(t, events)
	ts := newTestProxy(t, up.URL(), defTimeouts())

	_, body := postChat(t, ts.URL, streamChatBody)
	if body != joinEvents(events) {
		t.Fatalf("字节不等价:\n got=%q\nwant=%q", body, joinEvents(events))
	}
}

func TestStream_mid_stream_cut_断言出口_流内错误帧加DONE(t *testing.T) {
	up := testkit.StartStreamUpstream(t, testkit.MidStreamCutEvents())
	ts := newTestProxy(t, up.URL(), defTimeouts())

	_, body := postChat(t, ts.URL, streamChatBody)
	// 断言出口:已转发部分原样 + 流内错误帧 + [DONE] 干净收尾
	if !strings.HasSuffix(body, "data: [DONE]\n\n") {
		t.Fatalf("断言出口必须补发 [DONE]: %q", body)
	}
	if !strings.Contains(body, `"code":"mid_stream_fail"`) {
		t.Fatalf("流内错误帧 code 不符: %q", body)
	}
	if !strings.Contains(body, `"content":"部分"`) {
		t.Fatalf("已转发部分丢失: %q", body)
	}
}

func TestStream_slow_chunk_帧间超时_断言出口(t *testing.T) {
	up := testkit.StartStreamUpstream(t, testkit.SlowChunkEvents(400*time.Millisecond))
	ts := newTestProxy(t, up.URL(), Timeouts{Dial: time.Second, FirstToken: 5 * time.Second, ChunkIdle: 80 * time.Millisecond})

	start := time.Now()
	resp, body := postChat(t, ts.URL, streamChatBody)
	if time.Since(start) > 3*time.Second {
		t.Fatalf("帧间超时未及时触发(耗时 %v)", time.Since(start))
	}
	if resp.StatusCode != 200 {
		t.Fatalf("断言出口已过首 token,状态必须仍是 200(流已开),实际 %d", resp.StatusCode)
	}
	if !strings.Contains(body, `"code":"mid_stream_fail"`) || !strings.HasSuffix(body, "data: [DONE]\n\n") {
		t.Fatalf("帧间超时断言出口形态不符: %q", body)
	}
}

func TestStream_first_token超时_未产出任何输出_返回超时信封(t *testing.T) {
	// 首个内容帧迟迟不来:未过首 token,对客户端零可见流输出 → 归一化超时信封
	events := testkit.SlowChunkEvents(5 * time.Second)
	up := testkit.StartStreamUpstream(t, events)
	ts := newTestProxy(t, up.URL(), Timeouts{Dial: time.Second, FirstToken: 150 * time.Millisecond, ChunkIdle: time.Second})

	resp, body := postChat(t, ts.URL, streamChatBody)
	if resp.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("首 token 超时应 504,实际 %d: %s", resp.StatusCode, body)
	}
	var env protocol.ErrorEnvelope
	if err := unmarshalEnvelope(body, &env); err != nil {
		t.Fatalf("响应不是合法信封: %v (%s)", err, body)
	}
	if env.Error.Type != protocol.TypeTimeout {
		t.Fatalf("信封类型应为 timeout: %+v", env.Error)
	}
	if strings.Contains(body, `"content"`) {
		t.Fatalf("未过首 token 不得有任何内容输出: %s", body)
	}
}

func TestNonStream_json_ok_整包透传(t *testing.T) {
	up := testkit.StartJSONUpstream(t, 200, testkit.JSONOKBody)
	ts := newTestProxy(t, up.URL(), defTimeouts())

	resp, body := postChat(t, ts.URL, chatBody)
	if resp.StatusCode != 200 {
		t.Fatalf("状态码 %d", resp.StatusCode)
	}
	if body != testkit.JSONOKBody {
		t.Fatalf("整包透传被改写:\n got=%q\nwant=%q", body, testkit.JSONOKBody)
	}
}

func TestNonStream_上游400_归一信封直接返回不换道(t *testing.T) {
	up := testkit.StartJSONUpstream(t, 400, `{"error":{"message":"max_tokens is too large","type":"invalid_request_error"}}`)
	ts := newTestProxy(t, up.URL(), defTimeouts())

	resp, body := postChat(t, ts.URL, chatBody)
	if resp.StatusCode != 400 {
		t.Fatalf("上游 400 应原样映射 400,实际 %d: %s", resp.StatusCode, body)
	}
	var env protocol.ErrorEnvelope
	if err := unmarshalEnvelope(body, &env); err != nil {
		t.Fatalf("响应不是合法信封: %v (%s)", err, body)
	}
	if env.Error.Type != protocol.TypeBadRequest {
		t.Fatalf("类型不符: %+v", env.Error)
	}
	if !strings.Contains(env.Error.Message, "max_tokens is too large") {
		t.Fatalf("上游可读 message 未透传: %+v", env.Error)
	}
}

func TestNonStream_上游429_耗尽后如实说明(t *testing.T) {
	up := testkit.StartJSONUpstream(t, 429, `{"error":{"message":"rate limited"}}`)
	ts := newTestProxy(t, up.URL(), defTimeouts())

	resp, body := postChat(t, ts.URL, chatBody)
	if resp.StatusCode != 429 {
		t.Fatalf("上游 429 耗尽应保持 429,实际 %d: %s", resp.StatusCode, body)
	}
	var env protocol.ErrorEnvelope
	if err := unmarshalEnvelope(body, &env); err != nil || env.Error.Type != protocol.TypeRateLimited {
		t.Fatalf("信封不符: %v %+v", err, body)
	}
	if env.Error.Code != "all_candidates_failed" || !strings.Contains(env.Error.Message, "已尝试全部候选") {
		t.Fatalf("429 类耗尽须如实说明: %+v", env.Error)
	}
}

func TestNonStream_上游5xx_归一信封直返(t *testing.T) {
	up := testkit.StartJSONUpstream(t, 500, testkit.JSON500Body)
	ts := newTestProxy(t, up.URL(), defTimeouts())

	resp, body := postChat(t, ts.URL, chatBody)
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("上游 5xx 应归一为 502,实际 %d: %s", resp.StatusCode, body)
	}
	var env protocol.ErrorEnvelope
	if err := unmarshalEnvelope(body, &env); err != nil {
		t.Fatalf("信封非法: %v", err)
	}
	if env.Error.Type != protocol.TypeUpstreamError || env.Error.Code != "all_candidates_failed" {
		t.Fatalf("5xx 耗尽信封不符: %+v", env.Error)
	}
}

func TestNonStream_dial失败_候选耗尽信封(t *testing.T) {
	// 关停一个 server 制造 dial_fail
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close()

	ts := newTestProxy(t, deadURL, defTimeouts())
	resp, body := postChat(t, ts.URL, chatBody)
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("dial 失败耗尽应 502,实际 %d: %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, `"all_candidates_failed"`) {
		t.Fatalf("信封 code 不符: %s", body)
	}
}

func Test入站校验_未知别名与非法body(t *testing.T) {
	up := testkit.StartJSONUpstream(t, 200, testkit.JSONOKBody)
	ts := newTestProxy(t, up.URL(), defTimeouts())

	resp, body := postChat(t, ts.URL, `{"model":"no-such","messages":[]}`)
	if resp.StatusCode != 400 {
		t.Fatalf("未知别名应 400,实际 %d: %s", resp.StatusCode, body)
	}
	resp, body = postChat(t, ts.URL, `{invalid`)
	if resp.StatusCode != 400 {
		t.Fatalf("非法 body 应 400,实际 %d: %s", resp.StatusCode, body)
	}
	var env protocol.ErrorEnvelope
	if err := unmarshalEnvelope(body, &env); err != nil || env.Error.Type != protocol.TypeBadRequest {
		t.Fatalf("信封不符: %v %s", err, body)
	}
}

func TestStream_客户端断连_静默退出(t *testing.T) {
	up := testkit.StartStreamUpstream(t, testkit.SlowChunkEvents(500*time.Millisecond))
	ts := newTestProxy(t, up.URL(), Timeouts{Dial: time.Second, FirstToken: 5 * time.Second, ChunkIdle: time.Second})

	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "POST", ts.URL+"/v1/chat/completions", strings.NewReader(streamChatBody))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	// 读到响应头后立刻断连
	buf := make([]byte, 128)
	_, _ = resp.Body.Read(buf)
	cancel()
	_, _ = io.ReadAll(resp.Body) // 消费到连接关闭,不给 server 留悬挂写
	_ = resp.Body.Close()
	// 若 proxy 未正确传导 cancel,此处会因上游 500ms 剧本悬挂;超时即失败(由外层 10s 超时兜底)
}

// defTimeouts 常规测试超时(足够大,不触发)。
func defTimeouts() Timeouts {
	return Timeouts{Dial: 5 * time.Second, FirstToken: 10 * time.Second, ChunkIdle: 10 * time.Second}
}
