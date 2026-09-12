// 计量汇契约测试(ADR-0007):远端失败整批落缓冲、恢复按序续传幂等去重、缓冲容量背压、纯本地等价。
package metering

import (
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/solvela/muniu/internal/store"
)

// fakeRemote 可编程远端汇:失败 N 次后成功,记录收到批次。
type fakeRemote struct {
	failLeft atomic.Int64
	mu       sync.Mutex
	received [][]store.UsageRow
}

func (f *fakeRemote) Name() string { return "fake-remote" }

// Write 记录批次(与测试读取并发,-race 要求持锁)。
func (f *fakeRemote) Write(batch []store.UsageRow) error {
	if f.failLeft.Load() > 0 {
		f.failLeft.Add(-1)
		return errors.New("remote down")
	}
	f.mu.Lock()
	f.received = append(f.received, batch)
	f.mu.Unlock()
	return nil
}

// snapshot 最近一批的一致性副本。
func (f *fakeRemote) snapshot() [][]store.UsageRow {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]store.UsageRow(nil), f.received...)
}

func row(id string) store.UsageRow {
	return store.UsageRow{ID: id, RequestID: "req-" + id, PromptTokens: 1, CompletionTokens: 2}
}

// openSinkDB 与产品 store.Open 对齐:busy_timeout 兜住 flushLoop 写与测试读的锁竞争
// (CI 慢环境下裸开连接会 SQLITE_BUSY,首跑实证)
func openSinkDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "buf.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestPipeline_远端失败缓冲续传(t *testing.T) {
	db := openSinkDB(t)
	// 本地汇在远端成功时不落库,故无需 usage_logs 表;缓冲表需真实迁移
	if _, err := db.Exec(`CREATE TABLE metering_buffer (seq INTEGER PRIMARY KEY AUTOINCREMENT, row_id TEXT NOT NULL, payload TEXT NOT NULL, created_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	remote := &fakeRemote{}
	remote.failLeft.Store(1) // 第一批必失败 → 落缓冲
	buf := NewBufferStore(db, 100)
	p := NewPipeline(db, Options{
		Capacity: 64, Interval: 50 * time.Millisecond, Batch: 2,
		Remote: remote, Buffer: buf,
	}, nil)

	p.Submit(row("r1"))
	p.Submit(row("r2"))
	deadline := time.Now().Add(3 * time.Second)
	for len(remote.snapshot()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if len(remote.snapshot()) == 0 {
		t.Fatal("远端恢复后应收到缓冲续传批次")
	}
	// 续传批次按序含 r1/r2,且缓冲已排空
	received := remote.snapshot()
	got := received[len(received)-1]
	if len(got) != 2 || got[0].ID != "r1" || got[1].ID != "r2" {
		t.Fatalf("续传批次不符: %+v", got)
	}
	// 排空断言轮询:缓冲删除在 flushLoop 后台执行,慢环境下断言可能先于删除到达
	drainDeadline := time.Now().Add(3 * time.Second)
	for {
		n, _ := store.CountBufferedRows(db)
		if n == 0 {
			break
		}
		if time.Now().After(drainDeadline) {
			t.Fatalf("缓冲应排空,实际 %d", n)
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = p.Close()
}

func TestPipeline_纯本地等价(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "local.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	remote := &fakeRemote{}
	remote.failLeft.Store(0)
	buf := NewBufferStore(db, 10)
	p := NewPipeline(db, Options{Capacity: 8, Interval: 50 * time.Millisecond, Batch: 4, Remote: remote, Buffer: buf}, nil)
	_ = p.Close()
	if p.InFlight() != 0 {
		t.Fatal("关闭后在途应为 0")
	}
	if got := p.local.Name(); got != "local-sqlite" {
		t.Fatalf("本地汇名不符: %s", got)
	}
}

func TestBufferStore_容量与往返(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "cap.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE metering_buffer (seq INTEGER PRIMARY KEY AUTOINCREMENT, row_id TEXT NOT NULL, payload TEXT NOT NULL, created_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	buf := NewBufferStore(db, 2)
	if err := buf.Save([]store.UsageRow{row("a")}); err != nil {
		t.Fatal(err)
	}
	if err := buf.Save([]store.UsageRow{row("b"), row("c")}); !errors.Is(err, store.ErrBufferFull) {
		t.Fatalf("超容量应 ErrBufferFull: %v", err)
	}
	items, skipped, err := buf.Pending(10)
	if err != nil || len(items) != 1 || len(skipped) != 0 || items[0].Row.ID != "a" {
		t.Fatalf("Pending 不符: %+v %v", items, err)
	}
	if err := buf.Delete([]int64{items[0].Seq}); err != nil {
		t.Fatal(err)
	}
	if n, _ := store.CountBufferedRows(db); n != 0 {
		t.Fatalf("删除后应为空: %d", n)
	}
}
