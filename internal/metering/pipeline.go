// Package metering 异步写入用量；队列满时阻塞请求，关闭时排空队列。
// 用量可写入本地 SQLite 或远端汇；远端失败时整批暂存到本地。
package metering

import (
	"database/sql"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/solvela/muniu/internal/store"
)

// UsageRow 落库行，别名复用 store.UsageRow，不做二次定义。
type UsageRow = store.UsageRow

// Options 管道参数，零值取默认 4096/500ms/256。
// Remote 为 nil 时仅写入本地汇；
// Remote 非 nil 时 Buffer 必须非 nil，投递失败整批落本地缓冲、恢复后按序续传。
type Options struct {
	Capacity int           // channel 容量
	Interval time.Duration // 定时刷盘间隔
	Batch    int           // 攒满即刷阈值
	Remote   Sink          // 远端汇，nil 表示纯本地。
	Buffer   BufferStore   // 本地缓冲，Remote 非 nil 时必须。
	// OnSinkDown 远端汇投递失败落缓冲时的告警回调，可空。
	OnSinkDown func(sink string, rows int)
}

// Pipeline 计量异步落库管道，进程内单例，由装配层持有。
type Pipeline struct {
	opts   Options
	logger *slog.Logger
	local  *LocalSink
	remote Sink
	buffer BufferStore

	ch     chan UsageRow
	mu     sync.RWMutex // RLock 在途提交；Lock 关闭前排他等待，保证 close 无并发提交。
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
	if opts.Remote != nil && opts.Buffer == nil {
		// 装配层应保证；此处防呆降级为本地汇并显式告警，绝不因配置错而丢计量。
		logger.Error("计量远端汇缺少本地缓冲,降级为本地汇")
		opts.Remote = nil
	}
	p := &Pipeline{
		opts:   opts,
		logger: logger,
		local:  NewLocalSink(db),
		remote: opts.Remote,
		buffer: opts.Buffer,
		ch:     make(chan UsageRow, opts.Capacity),
		done:   make(chan struct{}),
	}
	go p.flushLoop()
	return p
}

// InFlight 管道在途水位，观测用，与 metrics 开关无关。
func (p *Pipeline) InFlight() int { return len(p.ch) }

// Submit 投递一行计量，每请求一行，含失败请求。
// channel 满时阻塞请求路径并先告警暴露背压，计量行是钱不能丢；
// 管道已关闭则丢弃并告警，绝不 panic。
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
		p.ch <- row // 持 RLock 阻塞：与 Close 的排他锁互斥，保证不会向已关闭 channel 发送。
	}
}

// Close 优雅关闭：等待在途提交全部入队 → 关闭入口 → 排干存量并落库 → 返回。
// 阻塞中的 Submit 会因 flusher 持续消费而完成，不会丢单。
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

// flushLoop 单 goroutine 消费：攒满 Batch 即刷，或到 Interval 定时刷；
// channel 关闭后排干剩余再退出。
func (p *Pipeline) flushLoop() {
	defer close(p.done)
	ticker := time.NewTicker(p.opts.Interval)
	defer ticker.Stop()

	batch := make([]UsageRow, 0, p.opts.Batch)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		p.writeOut(batch)
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
			if p.remote != nil {
				p.replay() // 定时排空待续传缓冲。
			}
		}
	}
}

// writeOut 无远端汇时写入本地；远端失败时暂存整批，成功时按序续传缓冲。
func (p *Pipeline) writeOut(batch []UsageRow) {
	if p.remote == nil {
		if err := p.local.Write(batch); err != nil {
			// 本地落库失败如实记日志：行已从管道取出无法回投；SQLite 单文件+OR IGNORE 下失败罕见。
			p.logger.Error("计量批量落库失败", "rows", len(batch), "error_detail", err.Error())
		}
		return
	}
	if err := p.remote.Write(batch); err != nil {
		p.logger.Error("计量远端投递失败,整批落本地缓冲",
			"sink", p.remote.Name(), "rows", len(batch), "error_detail", err.Error())
		if p.opts.OnSinkDown != nil {
			p.opts.OnSinkDown(p.remote.Name(), len(batch))
		}
		p.saveBuffered(batch)
		return
	}
	p.replay() // 远端恢复，排空历史缓冲。
}

// saveBuffered 落缓冲；容量满时阻塞重试，停止消费 channel，背压沿管道传导至请求路径。
// 每轮重试前先 best-effort 排空缓冲，远端已恢复时释放空间，死锁不可能成立。
// 非容量类错误如磁盘故障同样无限重试——计量行绝不丢优先于可用性，文案如实区分。
func (p *Pipeline) saveBuffered(batch []UsageRow) {
	for {
		if p.remote != nil {
			p.replay()
		}
		err := p.buffer.Save(batch)
		if err == nil {
			return
		}
		if errors.Is(err, store.ErrBufferFull) {
			p.logger.Error("计量缓冲已满,阻塞等待远端恢复排空", "rows", len(batch))
		} else {
			p.logger.Error("计量缓冲写入失败,阻塞重试(计量行绝不丢)", "rows", len(batch), "error_detail", err.Error())
		}
		time.Sleep(p.opts.Interval)
	}
}

// replay 按序续传缓冲：只读 → 投递 → 成功才删除；崩溃窗口由远端按 row_id 幂等去重兜底。
func (p *Pipeline) replay() {
	for {
		items, skipped, err := p.buffer.Pending(p.opts.Batch)
		if len(skipped) > 0 {
			p.logger.Error("计量缓冲存在损坏行(毒丸),已剔除并告警——该批计量无法续传",
				"skipped_seq", skipped)
		}
		if err != nil {
			p.logger.Error("读取计量缓冲失败", "error_detail", err.Error())
			return
		}
		if len(items) == 0 {
			return
		}
		rows := make([]UsageRow, 0, len(items))
		seqs := make([]int64, 0, len(items))
		for _, it := range items {
			rows = append(rows, it.Row)
			seqs = append(seqs, it.Seq)
		}
		if err := p.remote.Write(rows); err != nil {
			p.logger.Warn("计量缓冲续传失败,保持缓冲待下轮", "rows", len(rows), "error_detail", err.Error())
			return
		}
		if err := p.buffer.Delete(seqs); err != nil {
			p.logger.Error("缓冲续传后删除失败(远端幂等兜底,将重复投递)", "error_detail", err.Error())
			return
		}
	}
}
