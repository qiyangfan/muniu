// 宿主核心：注册期校验链 wasm magic → 编译 → 探测实例 → ABI 版本 → plugin_start，
// 请求体钩子单次调用编排 malloc→写→hook→读结果→free，以及 containment 分类。
package plugin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	wazero "github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/sys"
)

// Options 插件装配参数，缺省项取 ResourceDefaults。
type Options struct {
	Name             string
	Binary           []byte
	Config           map[string]string
	Enabled          bool // 注册后是否启用，管理面替换时保留原状态。
	FailOpen         bool // true 表示调用失败放行原请求，默认 fail_closed。
	TimeoutMs        int64
	MemoryLimitPages uint32
	PoolSize         int
	IdleTTLMs        int64
	ProbeTimeoutMs   int64 // 注册探测即实例化、版本、start 的超时；0 表示缺省取 2s 与 4×TimeoutMs 的较大值。
	Logger           *slog.Logger
}

// fill 补缺省并钳制硬顶，是 config 层与管理面校验之外的最后防线；
// 超限直达 wazero 会 panic，见 WithMemoryLimitPages 对超页数的处置。
func (o *Options) fill() {
	if o.TimeoutMs <= 0 {
		o.TimeoutMs = DefaultTimeoutMs
	}
	if o.TimeoutMs > MaxTimeoutMs {
		o.TimeoutMs = MaxTimeoutMs
	}
	if o.MemoryLimitPages == 0 {
		o.MemoryLimitPages = DefaultMemoryLimitPages
	}
	if o.MemoryLimitPages > MaxMemoryLimitPages {
		o.MemoryLimitPages = MaxMemoryLimitPages
	}
	if o.PoolSize <= 0 {
		o.PoolSize = DefaultPoolSize
	}
	if o.PoolSize > MaxPoolSize {
		o.PoolSize = MaxPoolSize
	}
	if o.IdleTTLMs <= 0 {
		o.IdleTTLMs = DefaultIdleTTLMs
	}
	if o.IdleTTLMs > MaxIdleTTLMs { // 极端值经 Duration 换算会溢出为负，回收循环静默不启动。
		o.IdleTTLMs = MaxIdleTTLMs
	}
	if o.ProbeTimeoutMs <= 0 {
		o.ProbeTimeoutMs = DefaultProbeTimeoutMs
		if o.TimeoutMs*4 > o.ProbeTimeoutMs {
			o.ProbeTimeoutMs = o.TimeoutMs * 4
		}
	}
	if o.ProbeTimeoutMs > MaxProbeTimeoutMs {
		o.ProbeTimeoutMs = MaxProbeTimeoutMs
	}
}

// Plugin 单个已注册插件：专属 runtime、编译产物与实例池。
type Plugin struct {
	opts     Options
	rt       wazero.Runtime
	compiled wazero.CompiledModule
	hostMod  api.Module
	pool     *instancePool

	inflight sync.WaitGroup
	closed   atomic.Bool

	// 快照持有者计数：Registry 替换或关停时，已取快照、尚未执行到本插件的在途请求
	// 由该计数守护，归零后才允许 Close，保证在途在旧版本上完整跑完；已进入
	// HookRequestBody 的调用由 inflight 守护。wasm 调用有超时上界，计数清零必然收敛。
	snapMu      sync.Mutex
	snapCond    *sync.Cond
	snapHolders int
}

func (p *Plugin) holdSnapshot() {
	p.snapMu.Lock()
	p.snapHolders++
	p.snapMu.Unlock()
}

func (p *Plugin) releaseSnapshotSlot() {
	p.snapMu.Lock()
	p.snapHolders--
	if p.snapHolders <= 0 {
		p.snapCond.Broadcast()
	}
	p.snapMu.Unlock()
}

// Compile 注册期校验与编译，任何一步失败即拒载，既有插件不受影响，
// 调用方持有的是各自 Plugin 实例。
func Compile(opts Options) (*Plugin, error) {
	if strings.TrimSpace(opts.Name) == "" {
		return nil, errors.New("插件名不能为空")
	}
	if err := checkWasmMagic(opts.Binary); err != nil {
		return nil, err
	}
	opts.fill()
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	opts.Logger = logger

	// 探测链必须有界：插件可在实例化、版本、start 任一环节死循环，Background ctx
	// 下 WithCloseOnContextDone 永不中断，Register 会永久挂死。
	probeCtx, cancelProbe := context.WithTimeout(context.Background(),
		time.Duration(opts.ProbeTimeoutMs)*time.Millisecond)
	defer cancelProbe()
	ctx := probeCtx
	p := &Plugin{opts: opts}
	p.snapCond = sync.NewCond(&p.snapMu)
	p.rt = newRuntime(opts.MemoryLimitPages)
	if err := wasiInstantiate(ctx, p.rt); err != nil {
		_ = p.rt.Close(ctx)
		return nil, fmt.Errorf("WASI 挂载失败: %w", err)
	}
	host, err := buildHostModule(ctx, p.rt, opts.Name, opts.Config, logger)
	if err != nil {
		_ = p.rt.Close(ctx)
		return nil, fmt.Errorf("宿主导入构建失败: %w", err)
	}
	p.hostMod = host
	compiled, err := p.rt.CompileModule(ctx, opts.Binary)
	if err != nil {
		_ = p.rt.Close(ctx)
		return nil, fmt.Errorf("非 wasm 或编译失败: %w", err)
	}
	p.compiled = compiled

	// 探测实例：版本探测与 start 校验，通过后直接入池零浪费。
	probe, err := p.newInstance(ctx)
	if err != nil {
		_ = p.rt.Close(ctx)
		return nil, fmt.Errorf("实例化失败: %w", err)
	}
	ret, err := probe.version.Call(ctx)
	if err != nil || len(ret) != 1 {
		_ = probe.mod.Close(ctx)
		_ = p.rt.Close(ctx)
		return nil, fmt.Errorf("ABI 版本探测失败: %w", err)
	}
	if int32(ret[0]) != ABIVersion {
		_ = probe.mod.Close(ctx)
		_ = p.rt.Close(ctx)
		return nil, fmt.Errorf("ABI 版本不匹配: 插件 %d,宿主 %d", int32(ret[0]), ABIVersion)
	}
	ret, err = probe.start.Call(ctx)
	if err != nil {
		_ = probe.mod.Close(ctx)
		_ = p.rt.Close(ctx)
		return nil, fmt.Errorf("plugin_start 调用失败: %w", err)
	}
	if len(ret) != 1 || int32(ret[0]) != 0 {
		code := int64(-1)
		if len(ret) == 1 {
			code = int64(int32(ret[0]))
		}
		_ = probe.mod.Close(ctx)
		_ = p.rt.Close(ctx)
		return nil, fmt.Errorf("plugin_start 失败(退出码 %d)", code)
	}

	p.pool = newPool(p, probe, time.Duration(opts.IdleTTLMs)*time.Millisecond)
	return p, nil
}

// checkWasmMagic 二进制合法性前置检查，校验 wasm magic 与版本字段，编译器兜底报错。
func checkWasmMagic(b []byte) error {
	if len(b) < 8 {
		return errors.New("二进制过短,非 wasm(需 ≥8 字节头部)")
	}
	if b[0] != 0x00 || b[1] != 0x61 || b[2] != 0x73 || b[3] != 0x6d {
		return errors.New("缺少 wasm magic(\\0asm),非 wasm 二进制")
	}
	if b[4] != 0x01 || b[5] != 0x00 || b[6] != 0x00 || b[7] != 0x00 {
		return errors.New("wasm 版本字段非法")
	}
	return nil
}

func errMissingExport(name string) error {
	return fmt.Errorf("缺少 ABI v0 必选导出: %s", name)
}

// Name 插件名。
func (p *Plugin) Name() string { return p.opts.Name }

// FailOpen 返回失败策略，fail_closed 为默认。
func (p *Plugin) FailOpen() bool { return p.opts.FailOpen }

// IsClosed 报告是否已下线，原子读，供运维观测与测试轮询；已下线后不得再调用钩子。
func (p *Plugin) IsClosed() bool { return p.closed.Load() }

// HookRequestBody 执行一次请求体钩子调用：从池取实例、编排调用，containment
// 统一出口在 settle 兑现指标、审计与 fail 策略。结论语义见 VerdictKind。
func (p *Plugin) HookRequestBody(parent context.Context, body []byte) Verdict {
	p.inflight.Add(1)
	defer p.inflight.Done()
	if p.closed.Load() {
		return p.settle(Verdict{Kind: VerdictKindFailed, Plugin: p.opts.Name}, ContainInstance, "插件已下线")
	}
	inst, err := p.pool.acquire(parent)
	if err != nil {
		return p.settle(Verdict{Kind: VerdictKindFailed, Plugin: p.opts.Name}, ContainInstance, err.Error())
	}
	v, kind, detail := p.call(parent, inst, body)
	if kind != "" {
		p.pool.poison(parent, inst)
	} else {
		p.pool.release(inst)
	}
	if kind != "" {
		return p.settle(v, kind, detail)
	}
	callsTotal.Inc(p.opts.Name, verdictLabel(v.Kind))
	callDuration.Observe(float64(time.Since(v.started).Milliseconds()))
	return v
}

// settle 是 containment 事件统一出口：计指标、写审计日志、按 fail 策略兑现结论。
func (p *Plugin) settle(v Verdict, kind ContainKind, detail string) Verdict {
	containmentTotal.Inc(p.opts.Name, string(kind))
	p.opts.Logger.Error("插件 containment",
		"plugin", p.opts.Name, "kind", string(kind), "error_detail", detail)
	if !v.started.IsZero() { // containment 调用同样计耗时；acquire 失败路径无起点不记。
		callDuration.Observe(float64(time.Since(v.started).Milliseconds()))
	}
	v.Plugin = p.opts.Name
	v.Body = nil
	if p.opts.FailOpen {
		// 放行决策必须留痕，Reason 携带 containment 摘要供日志消费方复核。
		v.Kind = VerdictKindPass
		v.Reason = string(kind) + ": " + detail
		callsTotal.Inc(p.opts.Name, verdictLabel(VerdictKindPass))
		return v
	}
	v.Kind = VerdictKindFailed
	v.Reason = string(kind) + ": " + detail
	callsTotal.Inc(p.opts.Name, verdictLabel(VerdictKindFailed))
	return v
}

// call 单次调用编排，返回结论、containment 分类与细节；kind 为空即调用成功。
func (p *Plugin) call(parent context.Context, inst *instance, body []byte) (Verdict, ContainKind, string) {
	started := time.Now()
	ctx, cancel := context.WithTimeout(parent, time.Duration(p.opts.TimeoutMs)*time.Millisecond)
	defer cancel()
	v := Verdict{Plugin: p.opts.Name, started: started}

	mem := inst.mod.Memory()
	// 入参 buffer 由宿主 malloc 后由宿主写入
	off, err := call1(ctx, inst.malloc, uint64(len(body)))
	if err != nil {
		k, d := classifyCallError(ctx, err)
		return v, k, d
	}
	if off == 0 {
		return v, ContainMalloc, "插件 malloc 返回 0(内存不足)"
	}
	freeTimeout := time.Duration(p.opts.TimeoutMs) * time.Millisecond
	if !mem.Write(uint32(off), body) {
		_ = freeOff(ctx, inst, off, freeTimeout) // 写已越界，free 失败不再改写结论。
		return v, ContainBadResult, fmt.Sprintf("入参写入越界(offset=%d len=%d)", off, len(body))
	}
	// 无论后续成败，入参与结果 buffer 都按同一分配器 free。
	hookRet, err := call1(ctx, inst.hook, uint64(off), uint64(len(body)))
	if err != nil {
		_ = freeOff(ctx, inst, off, freeTimeout)
		k, d := classifyCallError(ctx, err)
		return v, k, d
	}
	verdict := int32(hookRet)
	if ferr := freeOff(ctx, inst, off, freeTimeout); ferr != nil { // 入参 buffer 用毕即还。
		// free 被 wazero 中断即模块已关闭，实例不可复用，按 containment 处置。
		k, d := classifyCallError(ctx, ferr)
		return v, k, d
	}
	switch verdict {
	case VerdictPass:
		v.Kind = VerdictKindPass
		return v, "", ""
	case VerdictModified, VerdictReject:
	default:
		return v, ContainBadResult, fmt.Sprintf("verdict 非法: %d", verdict)
	}
	res, err := call1(ctx, inst.resultFn)
	if err != nil {
		k, d := classifyCallError(ctx, err)
		return v, k, d
	}
	resOff, resLen := unpackResult(res)
	if resLen < 0 || resLen > maxResultLen || resOff == 0 {
		return v, ContainBadResult, fmt.Sprintf("结果通道非法(offset=%d len=%d)", resOff, resLen)
	}
	// 结果 buffer 复用入参 buffer 原地改写时只 free 一次。
	resOwnsBuffer := int64(resOff) != off
	buf, ok := mem.Read(uint32(resOff), uint32(resLen))
	if !ok {
		if resOwnsBuffer {
			_ = freeOff(ctx, inst, int64(resOff), freeTimeout)
		}
		return v, ContainBadResult, fmt.Sprintf("结果读取越界(offset=%d len=%d)", resOff, resLen)
	}
	out := append([]byte(nil), buf...) // 拷出后立即释放插件内存。
	if resOwnsBuffer {
		if ferr := freeOff(ctx, inst, int64(resOff), freeTimeout); ferr != nil {
			k, d := classifyCallError(ctx, ferr)
			return v, k, d
		}
	}
	if verdict == VerdictModified {
		v.Kind = VerdictKindModified
		v.Body = out
	} else {
		v.Kind = VerdictKindRejected
		v.Reason = truncateReason(out)
	}
	return v, "", ""
}

// maxResultLen 结果通道单次上限 16 MiB，取请求体 32MB 上限的一半覆盖改写放大，
// 防止恶意插件把宿主拖入巨型拷贝。
const maxResultLen = 16 << 20

// call1 单返回值调用薄包装，wazero 返回 []uint64。
func call1(ctx context.Context, fn api.Function, args ...uint64) (int64, error) {
	ret, err := fn.Call(ctx, args...)
	if err != nil {
		return 0, err
	}
	if len(ret) == 0 {
		return 0, nil
	}
	return int64(ret[0]), nil
}

// freeOff 释放 buffer，独立短超时：free 与钩子同样运行不可信代码，WithoutCancel
// 只保证请求取消后清理仍执行，必须保留上界，否则恶意插件在 free 中死循环会让调用
// 永不返回，实例永不归还导致池满级联挂死，Close 的 inflight.Wait 随之卡死。
// 返回错误时模块已被 wazero 关闭，调用方必须丢弃该实例。
func freeOff(ctx context.Context, inst *instance, off int64, timeout time.Duration) error {
	if off == 0 || inst.freeFn == nil {
		return nil
	}
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()
	_, err := inst.freeFn.Call(fctx, uint64(off))
	return err
}

func truncateReason(b []byte) string {
	if len(b) > RejectReasonLimit {
		b = b[:RejectReasonLimit]
	}
	return string(b)
}

// classifyCallError 将调用失败映射为 containment 分类，即指标 label 语义：
// 超时 epoch 中断、主动 exit、越界超内存、其余 trap。
// ExitError 必须最先判定：wazero 把上下文取消与超时也包装成 ExitError，保留退出码
// 0xffffffff/0xefffffff，且其 Is 方法会把 0xffffffff 匹配到 context.Canceled，若先走
// errors.Is 分支，插件主动 proc_exit(-1) 会被误分类为超时。
// wazero 的取消编码与 proc_exit(0xffffffff) 错误值完全相同、不可由错误本身区分，
// 以 ctx.Err() 仲裁：调用上下文未取消时收到 0xffffffff 必为插件主动退出。
func classifyCallError(ctx context.Context, err error) (ContainKind, string) {
	var exitErr *sys.ExitError
	if errors.As(err, &exitErr) {
		switch exitErr.ExitCode() {
		case sys.ExitCodeContextCanceled:
			if ctx.Err() == nil { // 宿主未取消，插件主动 proc_exit(0xffffffff)。
				return ContainExit, err.Error()
			}
			return ContainTimeout, err.Error()
		case sys.ExitCodeDeadlineExceeded:
			return ContainTimeout, err.Error()
		default:
			return ContainExit, err.Error()
		}
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return ContainTimeout, err.Error()
	}
	if strings.Contains(err.Error(), "out of bounds") {
		return ContainOOM, err.Error()
	}
	return ContainTrap, err.Error()
}

func verdictLabel(k VerdictKind) string {
	switch k {
	case VerdictKindModified:
		return "modified"
	case VerdictKindRejected:
		return "rejected"
	case VerdictKindFailed:
		return "failed"
	default:
		return "pass"
	}
}

// Close 停止接收新调用，等待在途调用完成后关停池与 runtime。
// 热替换语义：新请求由新版本处理，在途请求在旧版本上完整跑完。
func (p *Plugin) Close() error {
	if p.closed.Swap(true) {
		return nil
	}
	p.inflight.Wait()
	p.pool.closeAll(context.Background())
	ctx := context.Background()
	if p.hostMod != nil {
		_ = p.hostMod.Close(ctx)
	}
	if p.compiled != nil {
		_ = p.compiled.Close(ctx)
	}
	return p.rt.Close(ctx)
}
