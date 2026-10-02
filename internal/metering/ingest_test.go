// ingest 汇传输边界测试(openspec recover-pending-usage-settlement):
// ① 2xx = 用量已持久接收——响应体携带钱包待结算统计(skipped_insufficient)仍是投递成功,
//
//	不解析、不转网络重试、不落缓冲,补账归控制面;
//
// ② 控制面持久接收批内前缀后存储故障回 5xx——网关整批保留并按原用量 ID 续传,
//
//	对侧按同一 ID 幂等去重,重复投递不重复计费。
//
// 钱包余额/账本状态仅作测试上下文,网关不消费任何财务字段。
package metering

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/solvela/muniu/internal/store"
)

// fakeControlPlane 按木牛 ingest 真实线格式({"rows":[...]},UsageRow PascalCase)收批。
// failFirstN>0:前 N 次请求在持久化批内前缀 persistPrefix 行后回 5xx(存储故障模拟);
// 计费与持久接收同源:同一用量 ID 只计费一次(控制面 refId 幂等的测试替身)。
type fakeControlPlane struct {
	mu            sync.Mutex
	failFirstN    int
	persistPrefix int // 失败请求也先持久接收的批内前缀行数
	attempts      int
	persisted     map[string]bool
	billed        map[string]int
	requestIDSeqs [][]string // 每次请求收到的用量 ID 序列(断言重放不换 ID)
	alwaysSkipped bool       // 2xx 响应体携带 skipped_insufficient>0(钱包待结算上下文)
}

func newFakeControlPlane(failFirstN, persistPrefix int) *fakeControlPlane {
	return &fakeControlPlane{
		failFirstN:    failFirstN,
		persistPrefix: persistPrefix,
		persisted:     map[string]bool{},
		billed:        map[string]int{},
	}
}

func (f *fakeControlPlane) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Rows []store.UsageRow `json:"rows"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.attempts++
		ids := make([]string, 0, len(req.Rows))
		for _, row := range req.Rows {
			ids = append(ids, row.ID)
		}
		f.requestIDSeqs = append(f.requestIDSeqs, ids)

		// 持久接收:失败请求只落批内前缀(其余行丢失,由网关整批重投兜底);
		// 已持久 ID 的重放不再计费(控制面同 ID 去重)
		limit := len(req.Rows)
		if f.attempts <= f.failFirstN && f.persistPrefix < limit {
			limit = f.persistPrefix
		}
		for _, row := range req.Rows[:limit] {
			if !f.persisted[row.ID] {
				f.persisted[row.ID] = true
				f.billed[row.ID]++
			}
		}
		if f.attempts <= f.failFirstN {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		// 2xx = 整批已持久接收;欠费行如实计入 skipped_insufficient(钱包待结算),
		// 不改变 HTTP 语义——网关据此结束投递,补账由控制面恢复任务完成
		skipped := 0
		if f.alwaysSkipped && len(req.Rows) > 0 {
			skipped = 1
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"accepted": len(req.Rows), "replayed": 0, "skipped_insufficient": skipped,
		})
	})
}

func (f *fakeControlPlane) snapshot() (attempts int, seqs [][]string, billed map[string]int, persisted map[string]bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.attempts, f.requestIDSeqs, f.billed, f.persisted
}

func openIngestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "ingest.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestIngestSink_2xx待结算不制造传输重试(t *testing.T) {
	cp := newFakeControlPlane(0, 0)
	cp.alwaysSkipped = true // 控制面已持久接收,部分行因余额不足进入待结算
	srv := httptest.NewServer(cp.handler())
	t.Cleanup(srv.Close)

	sink := &IngestSink{URL: srv.URL}
	if err := sink.Write([]store.UsageRow{row("w1"), row("w2")}); err != nil {
		t.Fatalf("2xx 含待结算行仍应视为投递成功: %v", err)
	}
	// 不重试、不改判网络失败:静默一个投递周期后请求次数仍为 1
	time.Sleep(150 * time.Millisecond)
	attempts, _, _, persisted := cp.snapshot()
	if attempts != 1 {
		t.Fatalf("待结算不制造传输重试,请求应恰 1 次,实际 %d", attempts)
	}
	if len(persisted) != 2 {
		t.Fatalf("控制面应已持久接收整批: %v", persisted)
	}
}

func TestPipeline_前缀已接收后5xx整批按原ID续传(t *testing.T) {
	db := openIngestDB(t)
	if _, err := db.Exec(`CREATE TABLE metering_buffer (seq INTEGER PRIMARY KEY AUTOINCREMENT, row_id TEXT NOT NULL, payload TEXT NOT NULL, created_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	// 首投:控制面持久接收批内前缀 1 行后存储故障回 5xx;重投:恢复,同 ID 去重
	cp := newFakeControlPlane(1, 1)
	srv := httptest.NewServer(cp.handler())
	t.Cleanup(srv.Close)

	p := NewPipeline(db, Options{
		Capacity: 64, Interval: 50 * time.Millisecond, Batch: 2,
		Remote: &IngestSink{URL: srv.URL}, Buffer: NewBufferStore(db, 100),
	}, nil)
	p.Submit(row("p1"))
	p.Submit(row("p2"))

	// 等缓冲排空(重投成功后删除)
	drain := time.Now().Add(5 * time.Second)
	for {
		n, _ := store.CountBufferedRows(db)
		if n == 0 && func() bool { a, _, _, _ := cp.snapshot(); return a >= 2 }() {
			break
		}
		if time.Now().After(drain) {
			a, _, _, _ := cp.snapshot()
			t.Fatalf("前缀丢失应由整批续传兜底,5s 内缓冲未排空: attempts=%d", a)
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = p.Close()

	attempts, seqs, billed, persisted := cp.snapshot()
	if attempts < 2 {
		t.Fatalf("应发生整批重投,请求次数 %d", attempts)
	}
	// 重投不换用量 ID:每次请求携带的都是原始 ID 序列的有序非空子序列
	//(网关不新建 ID、不丢行、不乱序;崩溃窗口由对侧去重兜底)。
	// 首次 flush 若被 50ms ticker 在两次 Submit 之间截开,首投会拆成 [p1]/[p2] 两批——
	// 这是管道既有批语义,不是续传契约违反,断言按子序列容忍以消除假红窗口
	original := []string{"p1", "p2"}
	seen := map[string]bool{}
	for i, seq := range seqs {
		if len(seq) == 0 {
			t.Fatalf("第 %d 次请求为空批", i+1)
		}
		pos := 0
		for _, id := range seq {
			for pos < len(original) && original[pos] != id {
				pos++
			}
			if pos == len(original) {
				t.Fatalf("第 %d 次请求出现未知或乱序 ID(不得换 ID): %v", i+1, seq)
			}
			pos++
			seen[id] = true
		}
	}
	for _, id := range original {
		if !seen[id] {
			t.Fatalf("原始行 %s 从未被投递", id)
		}
	}
	// 幂等去重:p1 首投已被前缀持久接收并计费,重投不重复计费
	if billed["p1"] != 1 || billed["p2"] != 1 {
		t.Fatalf("同一用量 ID 只计费一次: billed=%v", billed)
	}
	if len(persisted) != 2 {
		t.Fatalf("最终应完整接收两行: %v", persisted)
	}
}
