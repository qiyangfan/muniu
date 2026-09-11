// 计量落库链(metering.md「异步落库」契约):请求 goroutine → channel(cap 4096) →
// flusher 单 goroutine(500ms 或攒满 256 条 → 单事务批量 INSERT)。
// 背压优先于丢单:channel 满阻塞请求路径并告警,绝不 drop;优雅退出排干存量。
package metering

import (
	"database/sql"
	"log/slog"
	"sync"
	"time"

	"github.com/solvela/muniu/internal/store"
)

// UsageRow 落库行 = store.UsageRow(单一事实源,别名复用不做二次定义)。
type UsageRow = store.UsageRow

// Options 管道参数;零值取契约默认(4096/500ms/256)。
type Options struct {
	Capacity int           // channel 容量
	Interval time.Duration // 定时刷盘间隔
	Batch    int           // 攒满即刷阈值
}

// Pipeline 计量异步落库管道(进程内单例,由装配层持有)。
type Pipeline struct {
	db     *sql.DB
	opts   Options
	logger *slog.Logger

	ch     chan UsageRow
	mu     sync.RWMutex // RLock=在途提交;Lock=关闭前排他等待(保证 close 无并发提交)
	closed bool
	done   chan struct{} // flusher 退出信号
}

// NewPipeline 启动管道与 flusher goroutine。
func NewPipeline(db *sql.DB, opts Options, logger *slog.Logger) *Pipeline {
	if opts.Capacity <= 0 {
		opts.Capacity = 4096
	}
	if opts.Interval <= 0 {
		opts.Interval = 500 * time.Millisecond
	}
	if opts.Batch <= 0 {
		opts.Batch = 256
	}
	if logger == nil {
		logger = slog.Default()
	}
	p := &Pipeline{
		db: db, opts: opts, logger: logger,
		ch:   make(chan UsageRow, opts.Capacity),
		done: make(chan struct{}),
	}
	go p.flushLoop()
	return p
}

// Submit 投递一行计量(每请求一行,含失败请求)。
// channel 满:阻塞请求路径(计量行是钱)并先告警暴露背压;管道已关闭则丢弃并告警,绝不 panic。
func (p *Pipeline) Submit(row UsageRow) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.closed {
		p.logger.Warn("计量管道已关闭,计量行被丢弃", "request_id", row.RequestID)
		return
	}
	select {
	case p.ch <- row:
	default:
		p.logger.Warn("计量管道背压,请求路径阻塞等待落库", "request_id", row.RequestID, "capacity", p.opts.Capacity)
		p.ch <- row // 持 RLock 阻塞:与 Close 的排他锁互斥,保证不会向已关闭 channel 发送
	}
}

// Close 优雅关闭:等待在途提交全部入队 → 关闭入口 → 排干存量并落库 → 返回。
// 阻塞中的 Submit 会因 flusher 持续消费而完成,不会丢单。
func (p *Pipeline) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	close(p.ch)
	p.mu.Unlock()
	<-p.done
	return nil
}

// flushLoop 单 goroutine 消费:攒满 Batch 即刷,或到 Interval 定时刷;
// channel 关闭后排干剩余再退出(metering.md 背压与排干契约)。
func (p *Pipeline) flushLoop() {
	defer close(p.done)
	ticker := time.NewTicker(p.opts.Interval)
	defer ticker.Stop()

	batch := make([]UsageRow, 0, p.opts.Batch)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		if err := store.InsertUsageBatch(p.db, batch); err != nil {
			// 落库失败如实记日志:行已从管道取出,无法回投;SQLite 单文件+OR IGNORE 下失败罕见
			p.logger.Error("计量批量落库失败", "rows", len(batch), "error_detail", err.Error())
		}
		batch = batch[:0]
	}
	for {
		select {
		case row, ok := <-p.ch:
			if !ok {
				flush()
				return
			}
			batch = append(batch, row)
			if len(batch) >= p.opts.Batch {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}
