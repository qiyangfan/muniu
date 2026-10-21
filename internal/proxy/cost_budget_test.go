// 请求级成本预算行为：预检拒绝时上游零调用且不落计量行、流中中断时断言收尾且
// key 池零影响、计量如实落账、累计判定的真值覆盖与零预算旁路。
// 价目口径：输入 2 credit/tok、输出 8 credit/tok。
package proxy

import (
	"bytes"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"math"
	"math/big"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/solvela/muniu/internal/adapter"
	"github.com/solvela/muniu/internal/auth"
	"github.com/solvela/muniu/internal/config"
	"github.com/solvela/muniu/internal/keypool"
	"github.com/solvela/muniu/internal/metering"
	"github.com/solvela/muniu/internal/protocol"
	"github.com/solvela/muniu/internal/store"
	"github.com/solvela/muniu/internal/testkit"
)

// newBudgetServer 构造带价目与预算注入的网关，alias-a 价目为输入 2e6、输出 8e6 credit 每百万。
// 预算经 budgetSrc 间接读取，流中测试中断后调大预算可复用同网关，行为级证明 key 池无损。
func newBudgetServer(t *testing.T, up *testkit.Upstream, budgetSrc *int64) (*httptest.Server, *sql.DB) {
	t.Helper()
	reg := adapter.NewRegistry()
	reg.Register("prov-a", adapter.NewOpenAI(up.URL(), ""))
	db, err := store.Open(filepath.Join(t.TempDir(), "budget.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	pipeline := metering.NewPipeline(db, metering.Options{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { _ = pipeline.Close() })
	tbl, err := metering.NewPricingTable(
		metering.Pricing{Currency: "CNY", InputCreditsPerM: 2_000_000, OutputCreditsPerM: 8_000_000},
		time.UTC, nil,
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
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		info := &auth.KeyInfo{ID: "k1", TenantID: "t1", MaxRequestCostCredits: *budgetSrc}
		p.Handler().ServeHTTP(w, r.WithContext(auth.WithKeyInfo(r.Context(), info)))
	}))
	t.Cleanup(ts.Close)
	return ts, db
}

// assertNoRows 轮询断言计量零落账，预检拒绝不进 usage_logs。
func assertNoRows(t *testing.T, db *sql.DB) {
	t.Helper()
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM usage_logs`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatalf("预检拒绝不应落计量行,实际 %d 行", n)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// 预检闸：估算输入加 max_tokens 输出上界超预算即 402 cost_budget_exceeded，上游零调用、零落账。
func TestCostBudget_预检拒绝(t *testing.T) {
	up := testkit.StartJSONUpstream(t, 200, testkit.JSONOKBody)
	budget := int64(100) // 输出上界 1000 tok × 8 = 8000 credit ≫ 100
	ts, db := newBudgetServer(t, up, &budget)

	code, body := foPost(t, ts, `{"model":"alias-a","messages":[{"role":"user","content":"hi"}],"max_tokens":1000}`)
	if code != http.StatusPaymentRequired || !strings.Contains(body, "cost_budget_exceeded") {
		t.Fatalf("应 402 cost_budget_exceeded: %d %q", code, body)
	}
	if up.RequestCount() != 0 {
		t.Fatalf("预检拒绝应上游零调用,实际 %d", up.RequestCount())
	}
	assertNoRows(t, db)
}

// 流中闸：预检放行后流式累计超限即断言收尾，已生成内容保留、追加错误帧与 [DONE]；
// 中断后调大预算复用同网关同 key，请求照常成功，不计 key 失败、无冷却。
func TestCostBudget_流中中断与池无损(t *testing.T) {
	const chunk = "十二字测试内容十二字测试" // 每帧约 12+ token
	var frames strings.Builder
	for i := 0; i < 24; i++ {
		fmt.Fprintf(&frames, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", chunk)
	}
	events := []testkit.Event{{Data: frames.String() + "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"}}
	up := testkit.StartStreamUpstream(t, events)
	budget := int64(1000) // 预检：max_tokens 50×8=400 加输入 < 1000 放行；流式累计 24 帧 ≫ 1000
	ts, db := newBudgetServer(t, up, &budget)

	code, body := foPost(t, ts, `{"model":"alias-a","stream":true,"messages":[{"role":"user","content":"hi"}],"max_tokens":50}`)
	if code != 200 || !strings.Contains(body, "cost_budget_exceeded") || !strings.HasSuffix(body, "data: [DONE]\n\n") {
		t.Fatalf("应流内断言收尾: %d %q", code, body)
	}
	if !strings.Contains(body, chunk) {
		t.Fatalf("已开闸内容帧应保留: %q", body)
	}
	if up.RequestCount() != 1 {
		t.Fatalf("预算中断不应换道,实际上游被调 %d 次", up.RequestCount())
	}
	rows := waitRows(t, db, 1)
	if rows[0].Status != "error" || rows[0].ErrorClass != "cost_budget_exceeded" || !rows[0].Estimated {
		t.Fatalf("中断行应如实落账: %+v", rows[0])
	}

	// 池无损行为证明：同 key 调大预算后同剧本请求照常完整成功，无冷却、禁用、EMA 惩罚。
	budget = 1 << 40
	code, body = foPost(t, ts, `{"model":"alias-a","stream":true,"messages":[{"role":"user","content":"hi"}],"max_tokens":50}`)
	if code != 200 || strings.Contains(body, "cost_budget_exceeded") || !strings.HasSuffix(body, "data: [DONE]\n\n") {
		t.Fatalf("调大预算后应完整成功(key 池零影响): %d %q", code, body)
	}
}

// 累计判定单元：真值覆盖估算、零预算/无价目旁路。
func TestMeterState_budgetExceeded(t *testing.T) {
	prices := &metering.Pricing{InputCreditsPerM: 2_000_000, OutputCreditsPerM: 8_000_000}
	m := &meterState{budget: 100, budgetPrices: prices, budgetModel: "alias-a"}
	if m.budgetExceeded() {
		t.Fatal("零累计不应超限")
	}
	m.budgetEstIn = 30 // 估算输入 30 tok × 2 = 60 credit
	if m.budgetExceeded() {
		t.Fatal("60 credit < 预算 100 不应超限")
	}
	m.budgetOutEst = 10 // +估算输出 10 tok × 8 = 80 → 140
	if !m.budgetExceeded() {
		t.Fatal("140 credit > 预算 100 应超限")
	}
	// usage 帧真值覆盖估算，真值显著小于估算。
	m.budgetObserveUsage(protocol.Usage{PromptTokens: 5, CompletionTokens: 5}) // 10 + 40 = 50
	if m.budgetExceeded() {
		t.Fatal("真值覆盖后 50 credit 不应超限")
	}
	// 旁路格：预算 0 / 无价目。
	if (&meterState{budgetPrices: prices, budgetIn: 1 << 40}).budgetExceeded() {
		t.Fatal("预算 0 应旁路")
	}
	if (&meterState{budget: 1, budgetIn: 1 << 40}).budgetExceeded() {
		t.Fatal("无价目应旁路")
	}
}

func TestCostBudget_极大上界不能绕过预检(t *testing.T) {
	up := testkit.StartJSONUpstream(t, 200, testkit.JSONOKBody)
	budget := int64(100)
	ts, db := newBudgetServer(t, up, &budget)
	code, body := foPost(t, ts, `{"model":"alias-a","messages":[{"role":"user","content":"hi"}],"max_tokens":9223372036854775807}`)
	if code != 402 || !strings.Contains(body, "cost_budget_exceeded") || up.RequestCount() != 0 {
		t.Fatalf("上界整数溢出绕过: code=%d calls=%d body=%s", code, up.RequestCount(), body)
	}
	assertNoRows(t, db)
}

func TestCostBudget_输入usage不能吞掉中断输出(t *testing.T) {
	var frames strings.Builder
	frames.WriteString("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":0}}\n\n")
	for i := 0; i < 20; i++ {
		frames.WriteString("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n")
	}
	frames.WriteString("data: [DONE]\n\n")
	up := testkit.StartStreamUpstream(t, []testkit.Event{{Data: frames.String()}})
	budget := int64(30)
	ts, db := newBudgetServer(t, up, &budget)
	_, body := foPost(t, ts, `{"model":"alias-a","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if !strings.Contains(body, "cost_budget_exceeded") || !strings.Contains(body, "hello") {
		t.Fatal("没有发生已开闸中断", body)
	}
	rows := waitRows(t, db, 1)
	if rows[0].PromptTokens != 1 || rows[0].CompletionTokens <= 0 || !rows[0].Estimated || rows[0].CostCredits <= 2 {
		t.Fatalf("输入usage吞掉生成成本: %+v", rows[0])
	}
}

func TestCostBudget_usage之后的增量仍须检查(t *testing.T) {
	var frames strings.Builder
	frames.WriteString("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1}}\n\n")
	for i := 0; i < 20; i++ {
		frames.WriteString("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n")
	}
	frames.WriteString("data: [DONE]\n\n")
	up := testkit.StartStreamUpstream(t, []testkit.Event{{Data: frames.String()}})
	budget := int64(30)
	ts, db := newBudgetServer(t, up, &budget)
	_, body := foPost(t, ts, `{"model":"alias-a","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if !strings.Contains(body, "cost_budget_exceeded") {
		t.Fatal("旧usage真值压住后续增量，预算没有中断")
	}
	row := waitRows(t, db, 1)[0]
	if row.CompletionTokens <= 1 || !row.Estimated {
		t.Fatalf("最后usage之后生成部分漏记: %+v", row)
	}
}

func TestCostBudget_在途预算与落账不得跨价目(t *testing.T) {
	for _, tc := range []struct {
		name    string
		initial bool
		budget  int64
		want    int64
	}{
		{"启用预算", true, 100, 16},
		{"零预算", true, 0, 16},
		{"入口无价目", false, 100, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prices := func(n int64) *metering.PricingTable {
				tbl, err := metering.NewPricingTable(metering.Pricing{Currency: "CNY", InputCreditsPerM: n, OutputCreditsPerM: n}, time.UTC, nil)
				if err != nil {
					t.Fatal(err)
				}
				return tbl
			}
			initial := map[string]*metering.PricingTable{}
			if tc.initial {
				initial["alias-a"] = prices(1_000_000)
			}
			pricing := metering.NewPricingDirectory(initial)
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				pricing.Replace(map[string]*metering.PricingTable{"alias-a": prices(9_000_000)})
				_, _ = io.WriteString(w, testkit.JSONOKBody)
			}))
			defer up.Close()
			reg := adapter.NewRegistry()
			reg.Register("a", adapter.NewOpenAI(up.URL, ""))
			db, err := store.Open(filepath.Join(t.TempDir(), "snapshot.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			pipeline := metering.NewPipeline(db, metering.Options{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
			p := New(Config{Registry: reg, Pricing: pricing, Pipeline: pipeline, Routes: map[string][]config.Candidate{"alias-a": {{Provider: "a", Model: "up", Weight: 1}}}, Pools: map[string]*keypool.Pool{"a": keypool.NewPool([]keypool.KeyInput{{ID: "k", Value: "x", Weight: 1}})}, Timeouts: Timeouts{Dial: time.Second, FirstToken: time.Second}})
			r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"alias-a","messages":[{"role":"user","content":"hi"}],"max_tokens":10}`))
			r = r.WithContext(auth.WithKeyInfo(r.Context(), &auth.KeyInfo{MaxRequestCostCredits: tc.budget}))
			w := httptest.NewRecorder()
			p.Handler().ServeHTTP(w, r)
			if err := pipeline.Close(); err != nil {
				t.Fatal(err)
			}
			row := waitRows(t, db, 1)[0]
			if row.CostCredits != tc.want {
				t.Fatalf("入口预检用旧价，落账用了新价：%d", row.CostCredits)
			}

		})
	}
}

func TestBudgetCost_宽乘积与取整对拍(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	cases := [][4]uint64{
		{1, 1, 1, 1},
		{1_000_000_000_000_000, 0, 1_000_000, 0},
		{math.MaxInt64, 0, 1_000_000, 0},
		{math.MaxInt64, 1, 1_000_000, 1},
		{math.MaxUint64, 0, 1_000_000, 0},
	}
	for i := 0; i < 250; i++ {
		cases = append(cases, [4]uint64{r.Uint64(), r.Uint64(), uint64(r.Int63()), uint64(r.Int63())})
	}
	ceil := func(tokens, price uint64) *big.Int {
		product := new(big.Int).Mul(new(big.Int).SetUint64(tokens), new(big.Int).SetUint64(price))
		product.Add(product, big.NewInt(999_999))
		return product.Quo(product, big.NewInt(1_000_000))
	}
	for _, c := range cases {
		expected := new(big.Int).Add(ceil(c[0], c[2]), ceil(c[1], c[3]))
		got, valid := budgetCost(c[0], c[1], &metering.Pricing{InputCreditsPerM: int64(c[2]), OutputCreditsPerM: int64(c[3])})
		if valid != expected.IsInt64() || (valid && got != expected.Int64()) {
			t.Fatalf("宽乘积对拍失败: %v got=%d valid=%v want=%s", c, got, valid, expected.String())
		}
	}
}

func TestCostBudget_usage之后断流不得漏记(t *testing.T) {
	var frames strings.Builder
	frames.WriteString("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1}}\n\n")
	for i := 0; i < 20; i++ {
		frames.WriteString("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n")
	}
	up := testkit.StartStreamUpstream(t, []testkit.Event{{Data: frames.String()}})
	budget := int64(1_000_000)
	ts, db := newBudgetServer(t, up, &budget)
	foPost(t, ts, `{"model":"alias-a","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	row := waitRows(t, db, 1)[0]
	if row.CompletionTokens <= 1 || !row.Estimated {
		t.Fatalf("最后 usage 后的断流输出漏记: %+v", row)
	}
}

func TestMetering_费用溢出仍交付响应并留下核对证据(t *testing.T) {
	up := testkit.StartJSONUpstream(t, 200, `{"choices":[],"usage":{"prompt_tokens":1000000,"completion_tokens":1}}`)
	reg := adapter.NewRegistry()
	reg.Register("a", adapter.NewOpenAI(up.URL(), ""))
	tbl, err := metering.NewPricingTable(metering.Pricing{Currency: "USD", InputCreditsPerM: math.MaxInt64, OutputCreditsPerM: 1}, time.UTC, nil)
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(t.TempDir(), "overflow.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var log bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&log, nil))
	pipeline := metering.NewPipeline(db, metering.Options{}, logger)
	defer func() { _ = pipeline.Close() }()
	p := New(Config{Registry: reg, Pricing: metering.NewPricingDirectory(map[string]*metering.PricingTable{"alias-a": tbl}), Pipeline: pipeline, Logger: logger, Routes: map[string][]config.Candidate{"alias-a": {{Provider: "a", Model: "up", Weight: 1}}}, Pools: map[string]*keypool.Pool{"a": keypool.NewPool([]keypool.KeyInput{{ID: "k", Value: "fixture", Weight: 1}})}, Timeouts: Timeouts{Dial: time.Second, FirstToken: time.Second}})
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"alias-a","messages":[{"role":"user","content":"hi"}]}`))
	w := httptest.NewRecorder()
	p.Handler().ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"prompt_tokens":1000000`) {
		t.Fatalf("不得把计费异常改成出站失败: %d %s", w.Code, w.Body.String())
	}
	if err := pipeline.Close(); err != nil {
		t.Fatal(err)
	}
	row := waitRows(t, db, 1)[0]
	if row.Status != "error" || row.ErrorClass != metering.CostOverflowClass || !row.Estimated || row.CostCredits != 0 || row.PromptTokens != 1000000 || row.CompletionTokens != 1 {
		t.Fatalf("未结算观测丢失: %+v", row)
	}
	for _, field := range []string{`"level":"ERROR"`, `"prompt_tokens":1000000`, `"completion_tokens":1`, `"InputCreditsPerM":9223372036854775807`, `"OutputCreditsPerM":1`, `"estimated":true`} {
		if !strings.Contains(log.String(), field) {
			t.Fatalf("核对日志缺失 %s: %s", field, log.String())
		}
	}
}
