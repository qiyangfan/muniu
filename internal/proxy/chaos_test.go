//go:build chaos

// 故障演练(chaos,phase-3 T3.9,手动档 make chaos):
// 剧本「provider A 全部 key 连续 429 → 冷却 → 降级 priority 1 的 B 成功 → A 冷却到期恢复被重新选中」。
// 断言事件序列:usage 行序列 + key 池状态 + 降级期间的日志证据。
package proxy_test

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/solvela/muniu/internal/adapter"
	"github.com/solvela/muniu/internal/config"
	"github.com/solvela/muniu/internal/keypool"
	"github.com/solvela/muniu/internal/metering"
	"github.com/solvela/muniu/internal/proxy"
	"github.com/solvela/muniu/internal/store"
)

// TestChaos_429冷却降级与恢复 完整故障演练。
func TestChaos_429冷却降级与恢复(t *testing.T) {
	if os.Getenv("MUNIU_CHAOS") == "" {
		t.Skip("设置 MUNIU_CHAOS=1 以运行故障演练(make chaos)")
	}

	// 上游 A:恒 429,带 Retry-After: 1(短冷却便于演练)
	upA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "1")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
	}))
	t.Cleanup(upA.Close)
	// 上游 B:恒成功
	upB := testkitJSON(t, testkitJSONOKBody())
	t.Cleanup(upB.Close)

	reg := adapter.NewRegistry()
	reg.Register("prov-a", adapter.NewOpenAI(upA.URL, ""))
	reg.Register("prov-b", adapter.NewOpenAI(upB.URL, ""))
	poolA := keypool.NewPool([]keypool.KeyInput{{ID: "KA", Value: "sk-a", Weight: 1}})
	poolB := keypool.NewPool([]keypool.KeyInput{{ID: "KB", Value: "sk-b", Weight: 1}})

	db, err := store.Open(filepath.Join(t.TempDir(), "chaos.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	pipeline := metering.NewPipeline(db, metering.Options{Interval: time.Hour, Batch: 1 << 20, Capacity: 256},
		slog.New(slog.NewTextHandler(io.Discard, nil)))

	var logBuf lockedBuffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))

	p := proxy.New(proxy.Config{
		Registry: reg,
		Routes: map[string][]config.Candidate{
			"alias-a": {
				{Provider: "prov-a", Model: "up-a", Priority: 0, Weight: 1},
				{Provider: "prov-b", Model: "up-b", Priority: 1, Weight: 1},
			},
		},
		Timeouts: proxy.Timeouts{Dial: time.Second, FirstToken: 10 * time.Second, ChunkIdle: 10 * time.Second},
		Pipeline: pipeline,
		Pools:    map[string]*keypool.Pool{"prov-a": poolA, "prov-b": poolB},
		Logger:   logger,
	})
	ts := httptest.NewServer(p.Handler())
	t.Cleanup(ts.Close)

	post := func() (int, string) {
		resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json",
			strings.NewReader(`{"model":"alias-a","messages":[{"role":"user","content":"hi"}]}`))
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return resp.StatusCode, string(b)
	}

	// 第一轮:A 429(冷却 1s)→ 换道 B 成功
	if code, body := post(); code != 200 || !strings.Contains(body, "完整回答") {
		t.Fatalf("第一轮应降级 B 成功: %d %s", code, body)
	}
	now := time.Now()
	if st := poolA.StateOf("KA", now); st != "cooldown" {
		t.Fatalf("第一轮后 A 应冷却: %s", st)
	}

	// 第二轮:A 仍在冷却,直接 B(无 A 错误行)
	if code, _ := post(); code != 200 {
		t.Fatal("第二轮应 B 成功")
	}

	// 等 A 冷却到期(1s)+ 余量
	time.Sleep(1200 * time.Millisecond)
	if st := poolA.StateOf("KA", time.Now()); st != "healthy" {
		t.Fatalf("A 冷却应到期恢复: %s", st)
	}

	// 第三轮:恢复后的 A 被**重新选中**(priority 0 优先)→ 再次 429 → B 兜底
	if code, _ := post(); code != 200 {
		t.Fatal("第三轮应 B 兜底成功")
	}

	_ = pipeline.Close()

	// 断言 usage 行序列:A、B / B / A、B
	rows := queryAll(t, db)
	var seq []string
	for _, r := range rows {
		seq = append(seq, r.Provider)
	}
	want := "prov-a,prov-b|prov-b|prov-a,prov-b"
	if got := strings.Join(seq, ","); got != strings.ReplaceAll(want, "|", "\n") && got != strings.ReplaceAll(want, "|", ",") {
		if got != "prov-a,prov-b,prov-b,prov-a,prov-b" {
			t.Fatalf("事件序列不符: %v (期望 %s)", seq, want)
		}
	}
	// 日志证据:降级期间应有「key 池不可用跳过候选」
	if !strings.Contains(logBuf.String(), "key 池不可用") {
		t.Fatalf("缺少降级日志证据: %s", logBuf.String())
	}
}

// ---- 辅助(避免 chaos 构建标签依赖 testkit 的测试文件) ----

func testkitJSON(t *testing.T, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
}

func testkitJSONOKBody() string {
	return `{"id":"chaos","choices":[{"index":0,"message":{"role":"assistant","content":"完整回答"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func queryAll(t *testing.T, db *sql.DB) []store.UsageRow {
	t.Helper()
	rows, err := db.Query(`SELECT request_id, provider, model_alias, upstream_model, stream, status,
		IFNULL(error_class,''), prompt_tokens, completion_tokens, estimated, cost_credits, currency FROM usage_logs ORDER BY created_at`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []store.UsageRow
	for rows.Next() {
		var r store.UsageRow
		var stream int64
		if err := rows.Scan(&r.RequestID, &r.Provider, &r.ModelAlias, &r.UpstreamModel, &stream,
			&r.Status, &r.ErrorClass, &r.PromptTokens, &r.CompletionTokens,
			&r.Estimated, &r.CostCredits, &r.Currency); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

var (
	_ = json.Marshal
	_ = fmt.Sprintf
	_ atomic.Bool
)
