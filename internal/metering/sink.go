// 计量汇：落库端可插拔——本地 SQLite 为 standalone 默认，控制面 ingest 为 cluster 主路径。
// 批量写入、按行主键幂等；失败整批报错由调用方缓冲，计量行绝不丢。
package metering

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/solvela/muniu/internal/store"
)

// Sink 计量汇，批量写入；实现必须按行主键幂等。
type Sink interface {
	Name() string
	Write(batch []store.UsageRow) error
}

// LocalSink 将用量写入本地 SQLite。
type LocalSink struct {
	db  *sql.DB
	ctx context.Context
}

// NewLocalSink 构造本地汇。落库是记账性副作用：不随请求断连取消，也不随生命周期
// ctx(退出信号)取消——Close 的排干必须写完，故对装配 ctx 剥离取消。
func NewLocalSink(ctx context.Context, db *sql.DB) *LocalSink {
	return &LocalSink{db: db, ctx: context.WithoutCancel(ctx)}
}

// Name 汇名。
func (s *LocalSink) Name() string { return "local-sqlite" }

// Write 单事务批量落库，OR IGNORE 幂等。
func (s *LocalSink) Write(batch []store.UsageRow) error {
	return store.InsertUsageBatch(s.ctx, s.db, batch)
}

// defaultIngestClient 进程级共享以复用连接；批量投递是 500ms 级节奏，逐批新建徒增握手。
var defaultIngestClient = &http.Client{Timeout: 10 * time.Second}

// IngestSink 控制面 ingest 汇：POST JSON 批量；非 2xx 或网络失败整批报错，调用方落本地缓冲。
type IngestSink struct {
	Ctx   context.Context
	URL   string
	Token string
	// Client 可注入以便测试；nil 用共享默认超时客户端。
	Client *http.Client
}

// Name 汇名。
func (s *IngestSink) Name() string { return "control-plane-ingest" }

// Write 批量投递。
func (s *IngestSink) Write(batch []store.UsageRow) error {
	body, err := json.Marshal(map[string]any{"rows": batch})
	if err != nil {
		return fmt.Errorf("metering: 序列化投递批次: %w", err)
	}
	client := s.Client
	if client == nil {
		client = defaultIngestClient
	}
	// 单批超时兜底即可：退出信号不应把排干中的在途批次立即判死。
	ctx, cancel := context.WithTimeout(context.WithoutCancel(s.Ctx), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("metering: 构造投递请求: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if s.Token != "" {
		req.Header.Set("Authorization", "Bearer "+s.Token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("metering: ingest 投递失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("metering: ingest 投递状态 %d", resp.StatusCode)
	}
	return nil
}

// BufferStore 缓冲仓储接口，由 store 的 SQLite 实现承载；解耦便于测试。
type BufferStore interface {
	Save(batch []store.UsageRow) error // 容量满返回 store.ErrBufferFull，调用方阻塞重试形成背压。
	Pending(limit int) (rows []store.BufferedRow, skippedSeqs []int64, err error)
	Delete(seqs []int64) error
}

// bufferedStore SQLite 缓冲实现。
type bufferedStore struct {
	ctx      context.Context
	db       *sql.DB
	capacity int
}

// NewBufferStore 构造 SQLite 缓冲，capacity ≤0 视为 1。
func NewBufferStore(ctx context.Context, db *sql.DB, capacity int) BufferStore {
	if capacity <= 0 {
		capacity = 1
	}
	// 同 LocalSink：缓冲是关机排干的兜底落点，不随生命周期取消。
	return &bufferedStore{ctx: context.WithoutCancel(ctx), db: db, capacity: capacity}
}

func (b *bufferedStore) Save(batch []store.UsageRow) error {
	return store.SaveBufferedRows(b.ctx, b.db, b.capacity, batch)
}

func (b *bufferedStore) Pending(limit int) ([]store.BufferedRow, []int64, error) {
	return store.PendingBufferedRows(b.ctx, b.db, limit)
}

func (b *bufferedStore) Delete(seqs []int64) error {
	return store.DeleteBufferedRows(b.ctx, b.db, seqs)
}
