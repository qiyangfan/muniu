// metering 落库链测试：批量落库、攒满即刷、背压阻塞不丢、
// 优雅排干、tiktoken 离线估算路径。
package metering

import (
	"database/sql"
	"io"
	"log/slog"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/solvela/muniu/internal/protocol"
	"github.com/solvela/muniu/internal/store"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func openStore(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "metering.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func mkRow(id string) UsageRow {
	return UsageRow{
		ID: id, RequestID: "req-" + id, TenantID: "t1", APIKeyID: "k1",
		Provider: "deepseek", ModelAlias: "alias-a", UpstreamModel: "up-a",
		Status: "ok", PromptTokens: 100, CompletionTokens: 200,
		CostCredits: 8800, Currency: "CNY", LatencyTotalMs: 42,
		CreatedAt: time.Now().UTC(),
	}
}

func countRows(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM usage_logs`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPipeline_批量落库与优雅排干(t *testing.T) {
	db := openStore(t)
	p := NewPipeline(db, Options{Interval: time.Hour, Batch: 1 << 20, Capacity: 64}, testLogger()) // 不靠定时/批量阈值触发。
	for i := 0; i < 5; i++ {
		p.Submit(mkRow(string(rune('a' + i))))
	}
	if err := p.Close(); err != nil { // Close 必须排干存量后返回。
		t.Fatalf("Close: %v", err)
	}
	if got := countRows(t, db); got != 5 {
		t.Fatalf("排干后应 5 行,实际 %d", got)
	}
	p.Submit(mkRow("z")) // 关闭后提交：静默丢弃并告警，绝不 panic。
}

func TestPipeline_攒满Batch即刷不等定时(t *testing.T) {
	db := openStore(t)
	p := NewPipeline(db, Options{Interval: time.Hour, Batch: 8, Capacity: 64}, testLogger())
	for i := 0; i < 8; i++ {
		p.Submit(mkRow(string(rune('a' + i))))
	}
	// Batch=8，攒满即触发一次落库；给 flusher 一拍调度时间。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if countRows(t, db) == 8 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("攒满 8 条应立即落库,实际 %d", countRows(t, db))
}

func TestPipeline_背压阻塞不丢单(t *testing.T) {
	db := openStore(t)
	// Batch=1 时 flusher 每行都落库；占住唯一连接使落库阻塞 → channel 必然充满 → Submit 阻塞。
	p := NewPipeline(db, Options{Interval: time.Hour, Batch: 1, Capacity: 2}, testLogger())

	held, err := db.Begin() // 占住 MaxOpenConns(1) 的唯一连接，模拟慢盘。
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 4; i++ {
			p.Submit(mkRow(string(rune('a' + i))))
		}
	}()
	select {
	case <-done:
		t.Fatal("上游落库阻塞时,提交应当阻塞(背压语义失效)")
	case <-time.After(150 * time.Millisecond):
		// 预期：仍在阻塞。
	}
	_ = held.Rollback() // 释放连接，flusher 恢复排干。
	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Close 后被阻塞的提交应能完成(排干)")
	}
	if got := countRows(t, db); got != 4 {
		t.Fatalf("背压期间不得丢单,应 4 行,实际 %d", got)
	}
}

func TestTiktoken_离线词表与前缀映射(t *testing.T) {
	n, err := CountTokens("hello, world 这个是中文", "gpt-4o-mini")
	if err != nil || n <= 0 {
		t.Fatalf("o200k 前缀计数失败: n=%d err=%v", n, err)
	}
	n2, err := CountTokens("hello world", "gpt-3.5-turbo")
	if err != nil || n2 <= 0 {
		t.Fatalf("cl100k 前缀计数失败: n=%d err=%v", n2, err)
	}
	n3, err := CountTokens("你好世界", "deepseek-chat") // 未知模型 → 缺省 o200k_base。
	if err != nil || n3 <= 0 {
		t.Fatalf("未知模型缺省编码失败: n=%d err=%v", n3, err)
	}
}

func TestEstimate_请求输入与输出增量(t *testing.T) {
	in := estimateInputTokens([]map[string]any{
		{"role": "user", "content": "你好,请介绍一下木牛网关"},
	}, "deepseek-chat")
	if in <= 0 {
		t.Fatalf("输入估算必须为正: %d", in)
	}

	out := EstimateOutputTokens("这是已被转发的增量文本累计", "deepseek-chat")
	if out <= 0 {
		t.Fatalf("输出估算必须为正: %d", out)
	}
}

func TestEstimate_usuage缺失走估算并置位(t *testing.T) {
	// 上游 usage 缺失时走本地估算并置 estimated=true。
	in := estimateInputTokens([]map[string]any{{"role": "user", "content": "hi"}}, "m")
	out := EstimateOutputTokens("hello", "m")
	row := NewRow(RowParams{
		RequestID: "req-1", TenantID: "t1", APIKeyID: "k1",
		Provider: "p", ModelAlias: "alias", UpstreamModel: "up",
		Stream: true, Status: "ok",
		Usage:     protocol.Usage{PromptTokens: in, CompletionTokens: out},
		Estimated: true, TotalMs: 100,
	})
	if !row.Estimated {
		t.Fatal("估算行必须置位 estimated")
	}
	if row.PromptTokens != in || row.CompletionTokens != out {
		t.Fatalf("估算行字段不符: %+v", row)
	}
	if row.CostCredits < 0 {
		t.Fatal("成本不得为负")
	}
}

func TestNewRow_计价与价目币种(t *testing.T) {
	row := NewRow(RowParams{
		RequestID: "r", Status: "ok",
		Usage:   protocol.Usage{PromptTokens: 1200, CompletionTokens: 800},
		Pricing: &Pricing{Currency: "CNY", InputCreditsPerM: 2_000_000, OutputCreditsPerM: 8_000_000},
		TotalMs: 5,
	})
	if row.CostCredits != 8800 || row.Currency != "CNY" {
		t.Fatalf("计价不符: %d %s", row.CostCredits, row.Currency)
	}
	noPrice := NewRow(RowParams{RequestID: "r", Status: "error", ErrorClass: "timeout_first", TotalMs: 1})
	if noPrice.CostCredits != 0 || noPrice.ErrorClass != "timeout_first" {
		t.Fatalf("无价目行应零成本并带错误类: %+v", noPrice)
	}
}

func TestNewRow_成本溢出保留未结算观测(t *testing.T) {
	usage := protocol.Usage{PromptTokens: 1_000_000, CompletionTokens: 1}
	row := NewRow(RowParams{RequestID: "overflow", Status: "ok", Usage: usage, Pricing: &Pricing{Currency: "USD", InputCreditsPerM: math.MaxInt64, OutputCreditsPerM: 1}})
	if row.Status != "error" || row.ErrorClass != "cost_overflow" || !row.Estimated || row.CostCredits != 0 || row.Currency != "USD" || row.PromptTokens != usage.PromptTokens || row.CompletionTokens != usage.CompletionTokens {
		t.Fatalf("溢出被当正常账单或丢失原始 usage: %+v", row)
	}
}
