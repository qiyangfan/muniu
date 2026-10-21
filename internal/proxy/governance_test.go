// 覆盖模型能力治理的负面场景，并验证换道时从未修改的原始请求重新处理。
package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/solvela/muniu/internal/adapter"
	"github.com/solvela/muniu/internal/config"
	"github.com/solvela/muniu/internal/keypool"
	"github.com/solvela/muniu/internal/protocol"
	"github.com/solvela/muniu/internal/testkit"
)

func governReq(t *testing.T, body string) *protocol.ChatRequest {
	t.Helper()
	req, err := protocol.ParseChatRequest([]byte(body))
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	return req
}

// TestGovernChat_未声明模型零干预 验证未声明模型时透传字节不变、零变更零拒绝。
func TestGovernChat_未声明模型零干预(t *testing.T) {
	raw := `{"model":"alias","messages":[{"role":"user","content":"hi"}],"temperature":0.7,"frequency_penalty":0.5}`
	req := governReq(t, raw)

	governed, changes, reject := governChat(req, config.ModelCapability{}, false)
	if !bytes.Equal(governed.Raw, req.Raw) {
		t.Fatalf("未声明约束必须透传字节不变:\n 入 %s\n 出 %s", req.Raw, governed.Raw)
	}
	if governed.Temperature == nil || *governed.Temperature != 0.7 {
		t.Fatalf("结构字段不得被未声明治理触碰: %+v", governed.Temperature)
	}
	if len(changes) != 0 || reject != "" {
		t.Fatalf("零干预不得产生变更/拒绝: %+v %q", changes, reject)
	}
}

// TestGovernChat_仅干预声明覆盖的参数 验证已声明模型仅干预声明覆盖的参数。
func TestGovernChat_仅干预声明覆盖的参数(t *testing.T) {
	req := governReq(t, `{"model":"a","messages":[{"role":"user","content":"hi"}],"temperature":0.7,"frequency_penalty":0.5}`)

	governed, changes, reject := governChat(req, config.ModelCapability{Provider: "p", Model: "m", DropParams: []string{"temperature"}}, false)
	if reject != "" {
		t.Fatalf("宽松模式不拒绝: %q", reject)
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(governed.Raw, &out); err != nil {
		t.Fatalf("治理产物非法 JSON: %v", err)
	}
	if _, ok := out["temperature"]; ok {
		t.Fatal("声明 drop 的 temperature 必须被移除")
	}
	if _, ok := out["frequency_penalty"]; !ok {
		t.Fatal("未声明的 frequency_penalty 必须原样保留")
	}
	if len(changes) != 1 || changes[0].Param != "temperature" || changes[0].Action != "drop" {
		t.Fatalf("变更记录不符: %+v", changes)
	}
	if governed.Temperature != nil {
		// 结构字段同步是桥接型供应商 drop 生效的唯一通道，anthropic 消费 in.Temperature。
		t.Fatalf("副本结构字段必须同步删除,实际 %+v", governed.Temperature)
	}
}

// TestGovernChat_drop缺参零变更 验证声明了 drop 但请求未携带该参数时零变更。
func TestGovernChat_drop缺参零变更(t *testing.T) {
	req := governReq(t, `{"model":"a","messages":[{"role":"user","content":"hi"}]}`)
	governed, changes, _ := governChat(req, config.ModelCapability{Provider: "p", Model: "m", DropParams: []string{"temperature"}}, false)
	if !bytes.Equal(governed.Raw, req.Raw) || len(changes) != 0 {
		t.Fatalf("请求未携带被声明参数时必须零变更: %+v", changes)
	}
}

// TestGovernChat_clamp收敛与界内不变 验证越界上限收敛、界内值不变。
func TestGovernChat_clamp收敛与界内不变(t *testing.T) {
	capability := config.ModelCapability{Provider: "p", Model: "m", MaxOutputTokens: 4096}

	over := governReq(t, `{"model":"a","messages":[{"role":"user","content":"hi"}],"max_tokens":8192}`)
	governed, changes, _ := governChat(over, capability, false)
	var out struct {
		MaxTokens *int64 `json:"max_tokens"`
	}
	if err := json.Unmarshal(governed.Raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.MaxTokens == nil || *out.MaxTokens != 4096 {
		t.Fatalf("越界 max_tokens 必须收敛到 4096,实际 %+v", out.MaxTokens)
	}
	if governed.MaxTokens == nil || *governed.MaxTokens != 4096 {
		t.Fatalf("结构字段必须同步收敛(桥接消费 in.MaxTokens): %+v", governed.MaxTokens)
	}
	if *over.MaxTokens != 8192 {
		t.Fatalf("原始请求不得被污染(换道重治理的前提): %d", *over.MaxTokens)
	}
	if len(changes) != 1 || changes[0].Action != "clamp" {
		t.Fatalf("clamp 必须留痕: %+v", changes)
	}

	in := governReq(t, `{"model":"a","messages":[{"role":"user","content":"hi"}],"max_tokens":2048}`)
	governedIn, changesIn, _ := governChat(in, capability, false)
	if !bytes.Equal(governedIn.Raw, in.Raw) || len(changesIn) != 0 {
		t.Fatalf("界内值必须原样保留零变更: %+v", changesIn)
	}
}

// TestGovernChat_思考锁宽松归一 验证宽松模式归一移除关闭思考指令。
func TestGovernChat_思考锁宽松归一(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		dialect string // 被识别的关闭思考方言键
	}{
		{"qwen_enable_thinking", `{"model":"a","messages":[{"role":"user","content":"hi"}],"enable_thinking":false}`, "enable_thinking"},
		{"openai_reasoning_effort_minimal", `{"model":"a","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"minimal"}`, "reasoning_effort"},
		{"glm_thinking_disabled", `{"model":"a","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"disabled"}}`, "thinking"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := governReq(t, tc.body)
			governed, changes, reject := governChat(req, config.ModelCapability{Provider: "p", Model: "m", ThinkingLock: true}, false)
			if reject != "" {
				t.Fatalf("宽松模式不拒绝: %q", reject)
			}
			var out map[string]json.RawMessage
			if err := json.Unmarshal(governed.Raw, &out); err != nil {
				t.Fatal(err)
			}
			if _, ok := out[tc.dialect]; ok {
				t.Fatalf("关闭思考指令 %s 必须被移除", tc.dialect)
			}
			if len(changes) != 1 || changes[0].Action != "thinking_lock" || changes[0].Param != tc.dialect {
				t.Fatalf("思考锁干预必须留痕: %+v", changes)
			}
		})
	}
	// 思考开启意图不受影响：enable_thinking=true 不算关闭思考。
	req := governReq(t, `{"model":"a","messages":[{"role":"user","content":"hi"}],"enable_thinking":true}`)
	governed, changes, _ := governChat(req, config.ModelCapability{Provider: "p", Model: "m", ThinkingLock: true}, false)
	if !bytes.Equal(governed.Raw, req.Raw) || len(changes) != 0 {
		t.Fatalf("思考开启意图必须零干预: %+v", changes)
	}
}

// TestGovernChat_strict模式拒绝 验证 strict 下本应发生的干预升级为 400 拒绝，未命中放行。
func TestGovernChat_strict模式拒绝(t *testing.T) {
	req := governReq(t, `{"model":"a","messages":[{"role":"user","content":"hi"}],"temperature":0.7}`)
	governed, _, reject := governChat(req, config.ModelCapability{Provider: "p", Model: "m", DropParams: []string{"temperature"}}, true)
	if reject == "" {
		t.Fatal("strict 下命中 drop 必须拒绝")
	}
	if governed != nil {
		t.Fatal("拒绝路径不得产出治理请求")
	}
	if !bytes.Contains([]byte(reject), []byte("temperature")) {
		t.Fatalf("拒绝消息必须指明参数: %q", reject)
	}
	if !bytes.Contains([]byte(reject), []byte("p/m")) {
		t.Fatalf("拒绝消息必须指明目标模型(spec strict 场景): %q", reject)
	}

	// 声明了约束但请求未命中则放行。
	clean := governReq(t, `{"model":"a","messages":[{"role":"user","content":"hi"}]}`)
	governedClean, _, rejectClean := governChat(clean, config.ModelCapability{Provider: "p", Model: "m", DropParams: []string{"temperature"}}, true)
	if rejectClean != "" {
		t.Fatalf("strict 下未命中必须放行: %q", rejectClean)
	}
	if !bytes.Equal(governedClean.Raw, clean.Raw) {
		t.Fatal("放行路径字节不变")
	}

	// 思考锁 strict：关闭意图被拒。
	lock := governReq(t, `{"model":"a","messages":[{"role":"user","content":"hi"}],"enable_thinking":false}`)
	if _, _, r := governChat(lock, config.ModelCapability{Provider: "p", Model: "m", ThinkingLock: true}, true); r == "" {
		t.Fatal("strict 下思考锁命中必须拒绝")
	}
}

// TestGovernChat_换道重治理与幂等 验证换道后按新模型重新治理，不沿用首选收敛结果，且治理幂等。
func TestGovernChat_换道重治理与幂等(t *testing.T) {
	original := governReq(t, `{"model":"alias","messages":[{"role":"user","content":"hi"}],"max_tokens":8192,"temperature":0.7}`)

	// 首选模型：上限 4096 加 temperature 黑名单。
	capA := config.ModelCapability{Provider: "pa", Model: "ma", MaxOutputTokens: 4096, DropParams: []string{"temperature"}}
	first, _, _ := governChat(original, capA, false)
	var firstOut struct {
		MaxTokens *int64 `json:"max_tokens"`
	}
	_ = json.Unmarshal(first.Raw, &firstOut)
	if firstOut.MaxTokens == nil || *firstOut.MaxTokens != 4096 {
		t.Fatalf("首选模型治理不符: %+v", firstOut.MaxTokens)
	}

	// 备选模型仅上限 8192，治理对象仍是原始请求，不沿用首选收敛结果。
	capB := config.ModelCapability{Provider: "pb", Model: "mb", MaxOutputTokens: 8192}
	second, changesB, _ := governChat(original, capB, false)
	var secondOut struct {
		MaxTokens *int64 `json:"max_tokens"`
	}
	_ = json.Unmarshal(second.Raw, &secondOut)
	if secondOut.MaxTokens == nil || *secondOut.MaxTokens != 8192 {
		t.Fatalf("换道后必须按新模型重新治理(8192 界内不动),实际 %+v", secondOut.MaxTokens)
	}
	if len(changesB) != 0 {
		t.Fatalf("换道后界内零变更: %+v", changesB)
	}

	// 幂等：对治理结果再治理一次，无新变更且结果一致。
	again, changesAgain, _ := governChat(first, capA, false)
	if !bytes.Equal(again.Raw, first.Raw) || len(changesAgain) != 0 {
		t.Fatalf("治理必须幂等: %+v %s", changesAgain, again.Raw)
	}
}

// TestSummarize_多字节rune边界 验证切点落在多字节字符中间时回退到 rune 边界，
// 产出合法 UTF-8；字节硬切经 json.Marshal 会变 U+FFFD，日志乱码。
func TestSummarize_多字节rune边界(t *testing.T) {
	raw := json.RawMessage(`"` + strings.Repeat("木", 40) + `"`) // 40 个三字节字符 = 120 字节
	out := summarize(raw)
	if !utf8.ValidString(out) {
		t.Fatalf("摘要必须是合法 UTF-8: %q", out)
	}
	if !strings.HasSuffix(out, "…") || len(out) > 32+3 { // 截断体 + "…"
		t.Fatalf("摘要超长: %d 字节", len(out))
	}
}

// govOKBody 最小合法非流式成功响应，fake 上游回包。
const govOKBody = `{"id":"1","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`

// newGovHarness 治理集成测试台：候选与能力目录可配，返回 server 与两个 fake 上游并记录收到的 body。
// logger 可选注入，默认丢弃；留痕断言用例传 capture handler。
func newGovHarness(t *testing.T, capabilities []config.ModelCapability, strict bool, routes map[string][]config.Candidate, statusA int, loggers ...*slog.Logger) (*httptest.Server, map[string]*testkit.Upstream) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if len(loggers) > 0 && loggers[0] != nil {
		logger = loggers[0]
	}
	ups := map[string]*testkit.Upstream{}
	reg := adapter.NewRegistry()
	pools := map[string]*keypool.Pool{}
	for name, up := range map[string]*testkit.Upstream{
		"prov-a": testkit.StartJSONUpstream(t, statusA, `{"error":{"message":"boom"}}`),
		"prov-b": testkit.StartJSONUpstream(t, http.StatusOK, govOKBody),
	} {
		ups[name] = up
		reg.Register(name, adapter.NewOpenAI(up.URL(), ""))
		pools[name] = keypool.NewPool([]keypool.KeyInput{{ID: "K-" + name, Value: "sk-" + name, Weight: 1}})
	}
	p := New(Config{
		Registry:         reg,
		Routes:           routes,
		Timeouts:         Timeouts{Dial: time.Second, FirstToken: 5 * time.Second, ChunkIdle: 5 * time.Second},
		Pools:            pools,
		Capabilities:     capabilities,
		StrictGovernance: strict,
		RandIntn:         func(n int) int { return 0 },
		Logger:           logger,
	})
	ts := httptest.NewServer(p.Handler())
	t.Cleanup(ts.Close)
	return ts, ups
}

// govPost 发请求并返回状态码、body、响应头值，治理留痕断言需要响应头。
func govPost(t *testing.T, ts *httptest.Server, body string) (int, string, string) {
	t.Helper()
	resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode, string(b), resp.Header.Get("X-Muniu-Normalized")
}

// TestGovernance_换道后按新模型重新治理 验证每个候选按自身能力处理原始请求。
func TestGovernance_换道后按新模型重新治理(t *testing.T) {
	ts, ups := newGovHarness(t,
		[]config.ModelCapability{
			{Provider: "prov-a", Model: "up-a", MaxOutputTokens: 4096, DropParams: []string{"temperature"}},
			{Provider: "prov-b", Model: "up-b", MaxOutputTokens: 8192},
		},
		false,
		map[string][]config.Candidate{
			"alias": {
				{Provider: "prov-a", Model: "up-a", Priority: 0, Weight: 1},
				{Provider: "prov-b", Model: "up-b", Priority: 1, Weight: 1},
			},
		}, http.StatusInternalServerError)

	status, _, hdr := govPost(t, ts, `{"model":"alias","messages":[{"role":"user","content":"hi"}],"max_tokens":8192,"temperature":0.7}`)
	if status != http.StatusOK {
		t.Fatalf("换道后应成功: %d", status)
	}
	if hdr != "" {
		// 首选 prov-a 已留痕设头，换道到零干预的备选后必须清除，不得残留误导客户端。
		t.Fatalf("换道到零干预目标后不得残留干预头: %q", hdr)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(ups["prov-b"].LastBody(), &got); err != nil {
		t.Fatalf("备选收到的 body 非法: %v", err)
	}
	var mt int64
	_ = json.Unmarshal(got["max_tokens"], &mt)
	if mt != 8192 {
		t.Fatalf("换道后必须按新模型重新治理(8192 界内不动,不得沿用首选收敛的 4096),实际 %d", mt)
	}
	if _, ok := got["temperature"]; !ok {
		t.Fatal("备选未声明 drop,temperature 必须保留")
	}
}

// TestGovernance_干预留痕与出站改写 验证参数治理同时反映在上游请求、响应头和日志中。
func TestGovernance_干预留痕与出站改写(t *testing.T) {
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))
	ts, ups := newGovHarness(t,
		[]config.ModelCapability{
			{Provider: "prov-a", Model: "up-a", MaxOutputTokens: 4096, DropParams: []string{"temperature"}},
		},
		false,
		map[string][]config.Candidate{
			"alias": {{Provider: "prov-a", Model: "up-a", Priority: 0, Weight: 1}},
		}, http.StatusOK, logger)

	req, _ := http.Post(ts.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(`{"model":"alias","messages":[{"role":"user","content":"hi"}],"max_tokens":8192,"temperature":0.7}`))
	defer func() { _ = req.Body.Close() }()
	if req.StatusCode != http.StatusOK {
		t.Fatalf("宽松模式应放行: %d", req.StatusCode)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(ups["prov-a"].LastBody(), &got); err != nil {
		t.Fatal(err)
	}
	var mt int64
	_ = json.Unmarshal(got["max_tokens"], &mt)
	if mt != 4096 {
		t.Fatalf("出站 max_tokens 必须 clamp 到 4096,实际 %d", mt)
	}
	if _, ok := got["temperature"]; ok {
		t.Fatal("出站 temperature 必须被 drop")
	}
	hdr := req.Header.Get("X-Muniu-Normalized")
	set := map[string]bool{}
	for _, p := range strings.Split(hdr, ",") {
		set[strings.TrimSpace(p)] = true
	}
	if !set["temperature"] || !set["max_tokens"] || len(set) != 2 {
		t.Fatalf("响应头必须列出全部被干预参数,实际 %q", hdr)
	}
	logs := logBuf.String()
	for _, want := range []string{"请求参数治理干预", "provider=prov-a", "model=up-a", "param=temperature", "action=drop", "param=max_tokens", "action=clamp"} {
		if !strings.Contains(logs, want) {
			t.Fatalf("结构化日志缺 %q:\n%s", want, logs)
		}
	}
}

// TestGovernance_strict命中即400 验证 strict 模式在命中约束时返回 400 且不调用上游。
func TestGovernance_strict命中即400(t *testing.T) {
	ts, ups := newGovHarness(t,
		[]config.ModelCapability{
			{Provider: "prov-a", Model: "up-a", DropParams: []string{"temperature"}},
		},
		true,
		map[string][]config.Candidate{
			"alias": {{Provider: "prov-a", Model: "up-a", Priority: 0, Weight: 1}},
		}, http.StatusOK)

	status, body, _ := govPost(t, ts, `{"model":"alias","messages":[{"role":"user","content":"hi"}],"temperature":0.7}`)
	if status != http.StatusBadRequest {
		t.Fatalf("strict 命中必须 400,实际 %d", status)
	}
	if !strings.Contains(body, "param_rejected") || !strings.Contains(body, "temperature") {
		t.Fatalf("信封必须指明 code 与参数: %s", body)
	}
	if !strings.Contains(body, "prov-a/up-a") {
		t.Fatalf("信封必须指明目标模型(spec strict 场景): %s", body)
	}
	if n := ups["prov-a"].RequestCount(); n != 0 {
		t.Fatalf("拒绝路径不得触达上游,实际 %d 次", n)
	}

	okStatus, _, okHdr := govPost(t, ts, `{"model":"alias","messages":[{"role":"user","content":"hi"}]}`)
	if okStatus != http.StatusOK {
		t.Fatalf("未命中参数的请求必须放行: %d", okStatus)
	}
	if okHdr != "" {
		t.Fatalf("放行请求不得携带干预头: %q", okHdr)
	}
}
