// 覆盖桥接流式计量、断连计量、错误信封、回放切段和流式缓存组装。
package proxy

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/solvela/muniu/internal/adapter"
	_ "github.com/solvela/muniu/internal/adapter/bridge" // 接线 anthropic 桥接工厂
	"github.com/solvela/muniu/internal/config"
	"github.com/solvela/muniu/internal/keypool"
	"github.com/solvela/muniu/internal/metering"
	"github.com/solvela/muniu/internal/protocol"
	"github.com/solvela/muniu/internal/store"
	"github.com/solvela/muniu/internal/testkit"
)

// regressStreamUpstream 启动一个按剧本吐 SSE 的 fake 上游，payload 任意，原样封装为 data 事件。
func regressStreamUpstream(t *testing.T, payloads []string, lastDelay time.Duration) *testkit.Upstream {
	t.Helper()
	events := make([]testkit.Event, 0, len(payloads))
	for i, p := range payloads {
		ev := testkit.Event{Data: "data: " + p + "\n\n"}
		if i == len(payloads)-1 {
			ev.DelayBefore = lastDelay
		}
		events = append(events, ev)
	}
	return testkit.StartStreamUpstream(t, events)
}

// TestRegress_桥接流式usage计量 钉住桥接流式的 usage 抽取必须走适配器翻译帧：
// Anthropic 原始事件 usage 嵌套于 message 之下且键名不同，按原始 payload 抽取会整体归零落账。
func TestRegress_桥接流式usage计量(t *testing.T) {
	up := regressStreamUpstream(t, []string{
		`{"type":"message_start","message":{"usage":{"input_tokens":21,"cache_read_input_tokens":4}}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"答案"}}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":9}}`,
		`{"type":"message_stop"}`,
	}, 0)
	reg := adapter.NewRegistry()
	ad, err := adapter.For("anthropic", up.URL(), "sk-ant")
	if err != nil {
		t.Fatal(err)
	}
	reg.Register("prov-a", ad)
	poolA := keypool.NewPool([]keypool.KeyInput{{ID: "KA", Value: "sk-a", Weight: 1}})
	db, err := store.Open(filepath.Join(t.TempDir(), "bridge.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	pipeline := metering.NewPipeline(db, metering.Options{},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	p := New(Config{
		Registry: reg,
		Routes: map[string][]config.Candidate{
			"alias-a": {{Provider: "prov-a", Model: "claude-sonnet-4-5", Priority: 0, Weight: 1}},
		},
		Timeouts: Timeouts{Dial: time.Second, FirstToken: 10 * time.Second, ChunkIdle: 10 * time.Second},
		Pipeline: pipeline,
		Pools:    map[string]*keypool.Pool{"prov-a": poolA},
		RandIntn: func(n int) int { return 0 },
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ts := httptest.NewServer(p.Handler())
	t.Cleanup(ts.Close)

	code, body := foPost(t, ts, `{"model":"alias-a","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "答案") {
		t.Fatalf("桥接流应 200 且翻译内容就位: %d %q", code, body)
	}
	rows := waitRows(t, db, 1)
	r := rows[0]
	if r.Estimated || r.PromptTokens != 21 || r.CompletionTokens != 9 {
		t.Fatalf("桥接流式 usage 不得归零: estimated=%v prompt=%d completion=%d",
			r.Estimated, r.PromptTokens, r.CompletionTokens)
	}
}

// TestRegress_断连已开闸_估算计量且key免记失败 钉住判定表 context_canceled：
// 静默退出不换道不计失败，已过首 token 的已转发部分按估算计量。
func TestRegress_断连已开闸_估算计量且key免记失败(t *testing.T) {
	up := regressStreamUpstream(t, []string{
		`{"id":"1","choices":[{"index":0,"delta":{"role":"assistant"}}]}`,
		`{"id":"1","choices":[{"index":0,"delta":{"content":"断连前已转发的内容"}}]}`,
		`{"id":"1","choices":[],"usage":{"prompt_tokens":5,"completion_tokens":6}}`,
	}, 30*time.Second) // usage 帧迟迟不到，客户端在此之前断连
	reg := adapter.NewRegistry()
	reg.Register("prov-a", adapter.NewOpenAI(up.URL(), ""))
	poolA := keypool.NewPool([]keypool.KeyInput{{ID: "KA", Value: "sk-a", Weight: 1}})
	db, err := store.Open(filepath.Join(t.TempDir(), "cancel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	pipeline := metering.NewPipeline(db, metering.Options{},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	p := New(Config{
		Registry: reg,
		Routes: map[string][]config.Candidate{
			"alias-a": {{Provider: "prov-a", Model: "up-a", Priority: 0, Weight: 1}},
		},
		Timeouts: Timeouts{Dial: time.Second, FirstToken: 10 * time.Second, ChunkIdle: 20 * time.Second},
		Pipeline: pipeline,
		Pools:    map[string]*keypool.Pool{"prov-a": poolA},
		RandIntn: func(n int) int { return 0 },
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ts := httptest.NewServer(p.Handler())
	t.Cleanup(ts.Close)

	// 读到首个内容帧后立刻断连
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "POST", ts.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"alias-a","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4096)
	saw := 0
	for !strings.Contains(string(buf[:saw]), "断连前已转发的内容") {
		n, rerr := resp.Body.Read(buf[saw:])
		saw += n
		if rerr != nil || saw >= len(buf) {
			break
		}
	}
	cancel()
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	rows := waitRows(t, db, 1)
	if rows[0].Status != "client_cancel" || !rows[0].Estimated || rows[0].CompletionTokens <= 0 {
		t.Fatalf("已开闸断连应估算计量已发生部分: %+v", rows[0])
	}
	if r := poolA.EMARate("KA"); r < 0.99 {
		t.Fatalf("客户端断连不得记 key 失败,EMA 应保持健康: %v", r)
	}
}

// TestRegress_早退信封单次写出 钉住桥接无 embeddings 能力的早退路径必须只产出一个信封
// 且不再换道：双写会产生拼接 JSON，已写头再换道会产出 502 头加成功体。
func TestRegress_早退信封单次写出(t *testing.T) {
	up := testkit.StartJSONUpstream(t, 200, testkit.JSONOKBody)
	reg := adapter.NewRegistry()
	ad, err := adapter.For("anthropic", up.URL(), "sk-ant")
	if err != nil {
		t.Fatal(err)
	}
	reg.Register("prov-a", ad)
	db, err := store.Open(filepath.Join(t.TempDir(), "once.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	pipeline := metering.NewPipeline(db, metering.Options{},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	p := New(Config{
		Registry: reg,
		Routes: map[string][]config.Candidate{
			"emb-alias": {{Provider: "prov-a", Model: "up-emb", Priority: 0, Weight: 1}},
		},
		Timeouts: Timeouts{Dial: time.Second, FirstToken: 10 * time.Second, ChunkIdle: 10 * time.Second},
		Pipeline: pipeline,
		Pools:    map[string]*keypool.Pool{"prov-a": keypool.NewPool([]keypool.KeyInput{{ID: "KA", Value: "k", Weight: 1}})},
		RandIntn: func(n int) int { return 0 },
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ts := httptest.NewServer(p.HandlerEmbeddings())
	t.Cleanup(ts.Close)

	resp, err := http.Post(ts.URL+"/v1/embeddings", "application/json",
		strings.NewReader(`{"model":"emb-alias","input":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("应 400: %d", resp.StatusCode)
	}
	// 信封必须是单一合法 JSON：第二个 Decode 必须命中 EOF，双信封在此暴露。
	dec := json.NewDecoder(strings.NewReader(string(b)))
	var env protocol.ErrorEnvelope
	if err := dec.Decode(&env); err != nil {
		t.Fatalf("响应应为合法信封: %q %v", b, err)
	}
	if env.Error.Code != "embeddings_not_supported" {
		t.Fatalf("信封 code 应保留 embeddings_not_supported: %+v", env)
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		t.Fatalf("响应体出现第二个 JSON 值(双信封): %q", b)
	}
}

// TestRegress_回放切段rune边界 钉住语义缓存流式回放按字节切段不得劈开多字节字符：
// 非法 UTF-8 经 json.Marshal 被替换为 U+FFFD，命中回放的中文正文永久乱码。
func TestRegress_回放切段rune边界(t *testing.T) {
	content := strings.Repeat("木牛流式回放", 100) // 3 字节/字，64 字节边界必然落在字中间
	segs := splitForReplay(content, 64)
	joined := ""
	for _, s := range segs {
		if !utf8.ValidString(s) {
			t.Fatalf("切段产生非法 UTF-8: %q", s)
		}
		joined += s
	}
	if joined != content {
		t.Fatal("切段拼接后必须与原文逐字节等价")
	}
}

// TestRegress_流式缓存体拼装 钉住流式成功后按累计 content 拼标准响应体供缓存入库，
// 非 stop 终态与空内容不产出，宁缺勿错。
func TestRegress_流式缓存体拼装(t *testing.T) {
	norm := func(raw json.RawMessage) (protocol.Usage, error) {
		return protocol.Usage{PromptTokens: 21, CompletionTokens: 9}, nil
	}
	m := &meterState{normalize: norm, finishReason: "stop"}
	m.usageFrames = append(m.usageFrames, json.RawMessage(`{}`))
	m.contentOut.WriteString("你好,世界")
	body := m.assembleStreamBody("up-model")
	if body == nil {
		t.Fatal("stop+非空内容应产出缓存体")
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage protocol.Usage `json:"usage"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Choices) != 1 || parsed.Choices[0].Message.Content != "你好,世界" ||
		parsed.Choices[0].Message.Role != "assistant" || parsed.Choices[0].FinishReason != "stop" {
		t.Fatalf("缓存体形态不符: %s", body)
	}
	if parsed.Usage.PromptTokens != 21 || parsed.Usage.CompletionTokens != 9 {
		t.Fatalf("缓存体 usage 应并入上游帧: %+v", parsed.Usage)
	}

	// 非 stop 终态与空内容不产出，宁缺勿错。
	if m2 := (&meterState{finishReason: "length"}); m2.assembleStreamBody("m") != nil {
		t.Fatal("非 stop 终态不得入库")
	}
	if m3 := (&meterState{finishReason: "stop"}); m3.assembleStreamBody("m") != nil {
		t.Fatal("空内容不得入库")
	}
}

// TestRegress_SSE心跳不可见 空 data 与注释心跳不得把原始字节透给客户端，也不得触发断流出口；
// wireOf 必须先判适配器不可见，否则空载荷事件的 bytes.Equal 恒真会漏透原字节。
func TestRegress_SSE心跳不可见(t *testing.T) {
	events := []testkit.Event{
		{Data: ": keep-alive\n\n"},
		{Data: "data: \n\n"},
		{Data: "data: " + `{"id":"1","choices":[{"index":0,"delta":{"content":"正文"}}]}` + "\n\n"},
		{Data: "data: [DONE]\n\n"},
	}
	up := testkit.StartStreamUpstream(t, events)
	reg := adapter.NewRegistry()
	reg.Register("prov-a", adapter.NewOpenAI(up.URL(), ""))
	db, err := store.Open(filepath.Join(t.TempDir(), "ka.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	pipeline := metering.NewPipeline(db, metering.Options{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	p := New(Config{
		Registry: reg,
		Routes: map[string][]config.Candidate{
			"alias-a": {{Provider: "prov-a", Model: "up-a", Priority: 0, Weight: 1}},
		},
		Timeouts: Timeouts{Dial: time.Second, FirstToken: 10 * time.Second, ChunkIdle: 10 * time.Second},
		Pipeline: pipeline,
		Pools:    map[string]*keypool.Pool{"prov-a": keypool.NewPool([]keypool.KeyInput{{ID: "KA", Value: "k", Weight: 1}})},
		RandIntn: func(n int) int { return 0 },
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ts := httptest.NewServer(p.Handler())
	t.Cleanup(ts.Close)

	code, body := foPost(t, ts, `{"model":"alias-a","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "正文") || !strings.HasSuffix(body, "data: [DONE]\n\n") {
		t.Fatalf("心跳后的正常流应完整回放: %d %q", code, body)
	}
	if strings.Contains(body, "keep-alive") {
		t.Fatalf("注释心跳原始字节不得透传给客户端: %q", body)
	}
	// 空 data 事件同样不得透传，正常输出不含 data 加空行序列。
	if strings.Contains(body, "data: \n\n") {
		t.Fatalf("空 data 心跳不得透传给客户端: %q", body)
	}
}

// TestRegress_分时段倍率落账 验证分时段倍率会反映在最终计量行中。
func TestRegress_分时段倍率落账(t *testing.T) {
	up := testkit.StartJSONUpstream(t, 200, testkit.JSONOKBody)
	reg := adapter.NewRegistry()
	reg.Register("prov-a", adapter.NewOpenAI(up.URL(), ""))
	db, err := store.Open(filepath.Join(t.TempDir(), "tw.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	pipeline := metering.NewPipeline(db, metering.Options{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	tbl, err := metering.NewPricingTable(
		metering.Pricing{Currency: "CNY", InputCreditsPerM: 2_000_000, OutputCreditsPerM: 8_000_000},
		time.UTC,
		[]metering.WindowSpec{{StartMin: 0, EndMin: 1440, BasisPoints: 5000}}, // 全天半价
	)
	if err != nil {
		t.Fatal(err)
	}
	pricingDir := metering.NewPricingDirectory(map[string]*metering.PricingTable{"alias-a": tbl})
	p := New(Config{
		Registry: reg,
		Routes: map[string][]config.Candidate{
			"alias-a": {{Provider: "prov-a", Model: "up-a", Priority: 0, Weight: 1}},
		},
		Pricing:  pricingDir,
		Timeouts: Timeouts{Dial: time.Second, FirstToken: 10 * time.Second, ChunkIdle: 10 * time.Second},
		Pipeline: pipeline,
		Pools:    map[string]*keypool.Pool{"prov-a": keypool.NewPool([]keypool.KeyInput{{ID: "KA", Value: "k", Weight: 1}})},
		RandIntn: func(n int) int { return 0 },
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ts := httptest.NewServer(p.Handler())
	t.Cleanup(ts.Close)

	code, _ := foPost(t, ts, `{"model":"alias-a","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 {
		t.Fatalf("应 200: %d", code)
	}
	rows := waitRows(t, db, 1)
	// 基准 8600 credit 为 ceil(7×2e8/1e6)+ceil(9×8e8/1e6)，半价 4300。
	if rows[0].CostCredits != 43 {
		t.Fatalf("半价窗应落账 4300 credit(43μ×100),实际 %d", rows[0].CostCredits)
	}
}
