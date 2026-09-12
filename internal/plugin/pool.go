// 实例池(3.1):每插件池化 wasm 实例,并发复用、空闲回收、trap 后重建。
// 通道即池:借出=收,归还=发;live 计数约束总量;毒化实例(close)后异步补建,
// 等待中的 acquire 由补建实例唤醒——任何路径都不会永久卡死(ctx 兜底)。
package plugin

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	wazero "github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

// instance 单个已就绪的 wasm 实例(导出函数句柄预解析,热路径零查找)。
type instance struct {
	mod      api.Module
	version  api.Function
	start    api.Function
	malloc   api.Function
	freeFn   api.Function
	hook     api.Function
	resultFn api.Function
	lastUsed atomic.Int64 // unix nanos,空闲回收判据
}

// instancePool 池本体。
type instancePool struct {
	owner *Plugin

	mu      sync.Mutex
	live    int // 已创建未关闭(含借出)
	idle    int // 池内待借
	closing bool

	ch chan *instance // 待借实例(cap = poolSize,发送方唯一是归还/补建)

	createdTotal atomic.Int64
	closedTotal  atomic.Int64

	idleTTL  time.Duration
	stop     chan struct{}
	stopOnce sync.Once
	// kick 唤醒源:补建失败等「无人发实例但池有空间」的场景广播等待者重入循环
	// 重评 closing/live——否则等待者只能等 caller ctx,Close 链上可演变为死锁。
	kick chan struct{}
}

var errPoolClosed = errors.New("插件实例池已关闭")

// newInstance 用编译产物实例化一个 wasm 实例(资源限制经 ModuleConfig 施加)。
func (p *Plugin) newInstance(ctx context.Context) (*instance, error) {
	modCfg := wazero.NewModuleConfig().
		WithName("").                     // 匿名实例:同 runtime 可并存多个
		WithStartFunctions("_initialize") // 只跑 reactor 构造器;绝不跑 _start(Rust command main)
	mod, err := p.rt.InstantiateModule(ctx, p.compiled, modCfg)
	if err != nil {
		return nil, err
	}
	inst := &instance{mod: mod}
	for _, e := range []struct {
		name string
		dst  *api.Function
	}{
		{ExportVersion, &inst.version},
		{ExportStart, &inst.start},
		{ExportMalloc, &inst.malloc},
		{ExportFree, &inst.freeFn},
		{ExportHook, &inst.hook},
		{ExportResult, &inst.resultFn},
	} {
		*e.dst = mod.ExportedFunction(e.name)
		if *e.dst == nil {
			_ = mod.Close(ctx)
			return nil, errMissingExport(e.name)
		}
	}
	return inst, nil
}

func newPool(owner *Plugin, probe *instance, idleTTL time.Duration) *instancePool {
	pl := &instancePool{
		owner:   owner,
		ch:      make(chan *instance, owner.opts.PoolSize),
		idleTTL: idleTTL,
		stop:    make(chan struct{}),
		kick:    make(chan struct{}, 1),
	}
	pl.mu.Lock()
	pl.live = 1
	pl.idle = 1
	pl.mu.Unlock()
	probe.lastUsed.Store(time.Now().UnixNano())
	pl.ch <- probe
	pl.createdTotal.Store(1)
	if idleTTL > 0 {
		go pl.recycleLoop()
	}
	return pl
}

// acquire 取实例:优先复用池内空闲;未满则即建;满则等待归还或补建。
func (pl *instancePool) acquire(ctx context.Context) (*instance, error) {
	for {
		select {
		case inst := <-pl.ch:
			pl.mu.Lock()
			pl.idle--
			pl.mu.Unlock()
			inst.lastUsed.Store(time.Now().UnixNano())
			return inst, nil
		default:
		}
		pl.mu.Lock()
		if pl.closing {
			pl.mu.Unlock()
			return nil, errPoolClosed
		}
		if pl.live < pl.owner.opts.PoolSize {
			pl.live++
			pl.mu.Unlock()
			inst, err := pl.owner.newInstance(ctx)
			if err != nil {
				pl.mu.Lock()
				pl.live--
				pl.mu.Unlock()
				return nil, err
			}
			pl.createdTotal.Add(1)
			inst.lastUsed.Store(time.Now().UnixNano())
			return inst, nil
		}
		pl.mu.Unlock()
		// 池满:等归还/补建;Close(kick 关闭广播)与补建失败(kick 单发)也必须能唤醒
		select {
		case inst := <-pl.ch:
			pl.mu.Lock()
			pl.idle--
			pl.mu.Unlock()
			inst.lastUsed.Store(time.Now().UnixNano())
			return inst, nil
		case <-pl.kick:
			continue // 重入循环:重评 closing / live<PoolSize
		case <-pl.stop:
			return nil, errPoolClosed
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// release 归还健康实例。
func (pl *instancePool) release(inst *instance) {
	inst.lastUsed.Store(time.Now().UnixNano())
	pl.mu.Lock()
	pl.idle++
	pl.mu.Unlock()
	pl.ch <- inst
}

// poison 丢弃故障实例(close)+异步补建占位,保证池满等待者能被唤醒。
func (pl *instancePool) poison(ctx context.Context, inst *instance) {
	_ = inst.mod.Close(context.WithoutCancel(ctx))
	pl.closedTotal.Add(1)
	pl.mu.Lock()
	pl.live--
	pl.mu.Unlock()
	// 补建:失败不重试(下一次 acquire 会再建),但必须 kick 唤醒等待者重入循环
	pl.mu.Lock()
	canFill := !pl.closing && pl.live < pl.owner.opts.PoolSize
	if canFill {
		pl.live++
	}
	pl.mu.Unlock()
	if !canFill {
		return
	}
	go func() {
		newInst, err := pl.owner.newInstance(context.Background())
		if err != nil {
			pl.mu.Lock()
			closing := pl.closing
			pl.live--
			pl.mu.Unlock()
			if !closing { // 池仍有空间却建不出:广播等待者自己重试/重评
				select {
				case pl.kick <- struct{}{}:
				default:
				}
			}
			return
		}
		pl.createdTotal.Add(1)
		newInst.lastUsed.Store(time.Now().UnixNano())
		pl.mu.Lock()
		if pl.closing { // 与 closeAll 竞态:已关停则不留新实例
			pl.live--
			pl.mu.Unlock()
			_ = newInst.mod.Close(context.Background())
			pl.closedTotal.Add(1)
			return
		}
		pl.idle++
		pl.mu.Unlock()
		pl.ch <- newInst
	}()
}

// recycleLoop 空闲回收:周期把超时未用的空闲实例关掉(design Risks:实例池内存驻留)。
func (pl *instancePool) recycleLoop() {
	interval := pl.idleTTL / 2
	if interval < 10*time.Millisecond {
		interval = 10 * time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-pl.stop:
			return
		case <-ticker.C:
		}
		now := time.Now().UnixNano()
		var keep []*instance
	drain:
		for {
			select {
			case inst := <-pl.ch:
				pl.mu.Lock()
				pl.idle-- // 出池
				if now-inst.lastUsed.Load() > pl.idleTTL.Nanoseconds() {
					pl.mu.Unlock()
					_ = inst.mod.Close(context.Background())
					pl.closedTotal.Add(1)
					pl.mu.Lock()
					pl.live--
					pl.mu.Unlock()
					continue
				}
				pl.mu.Unlock()
				keep = append(keep, inst) // 未超时,先收起——放回必须在本轮 drain 结束后,
			default: // 否则同一实例被反复弹出形成自旋
				break drain
			}
		}
		for _, inst := range keep {
			pl.mu.Lock()
			pl.idle++
			pl.mu.Unlock()
			pl.ch <- inst
		}
	}
}

// closeAll 关停:拒新借出,关掉池内与借出无法追踪外的全部实例。
// 借出实例由 Plugin.Close 的 inflight 等待保证先归还。
func (pl *instancePool) closeAll(ctx context.Context) {
	pl.stopOnce.Do(func() { close(pl.stop) })
	pl.mu.Lock()
	pl.closing = true
	remaining := make([]*instance, 0, pl.idle)
	pl.idle = 0
	pl.mu.Unlock()
drain:
	for {
		select {
		case inst := <-pl.ch:
			remaining = append(remaining, inst)
		default:
			break drain
		}
	}
	for _, inst := range remaining {
		_ = inst.mod.Close(ctx)
		pl.closedTotal.Add(1)
		pl.mu.Lock()
		pl.live--
		pl.mu.Unlock()
	}
}

// idleSnapshot 池内空闲实例数(指标抓取时一致性要求弱,锁内快照即可)。
func (pl *instancePool) idleSnapshot() int {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	return pl.idle
}
