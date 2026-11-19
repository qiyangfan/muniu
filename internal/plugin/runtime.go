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
	o.TimeoutMs = clampResource(o.TimeoutMs, DefaultTimeoutMs, MaxTimeoutMs)
	o.MemoryLimitPages = clampResource(o.MemoryLimitPages, DefaultMemoryLimitPages, MaxMemoryLimitPages)
	o.PoolSize = clampResource(o.PoolSize, DefaultPoolSize, MaxPoolSize)
	// IdleTTLMs 极端值经 Duration 换算会溢出为负，回收循环静默不启动。
	o.IdleTTLMs = clampResource(o.IdleTTLMs, DefaultIdleTTLMs, MaxIdleTTLMs)

	// 探测超时缺省取 2s 与 4×TimeoutMs 的较大值，再钳硬顶。
	probeDefault := int64(DefaultProbeTimeoutMs)
	if o.TimeoutMs*4 > probeDefault {
		probeDefault = o.TimeoutMs * 4
	}
	o.ProbeTimeoutMs = clampResource(o.ProbeTimeoutMs, probeDefault, MaxProbeTimeoutMs)
}

// clampResource 统一「≤0 取缺省、超硬顶钳制」语义；调用方保证不同类型各自专有，
// 经实例化推断 T，调用点不显式写类型参数。
func clampResource[T int | int64 | uint32](v, def, max T) T {
	if v <= 0 {
		return def
	}
	if v > max {
		return max
	}
	return v
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
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}

	// 探测链必须有界：插件可在实例化、版本、start 任一环节死循环，Background ctx
	// 下 WithCloseOnContextDone 永不中断，Register 会永久挂死。
	ctx, cancelProbe := context.WithTimeout(context.Background(),
		time.Duration(opts.ProbeTimeoutMs)*time.Millisecond)
	defer cancelProbe()

	p := &Plugin{opts: opts}
	p.snapCond = sync.NewCond(&p.snapMu)
	p.rt = newRuntime(opts.MemoryLimitPages)
	if err := p.setupRuntime(ctx); err != nil {
		return nil, err
	}
	// 探测实例：版本探测与 start 校验，通过后直接入池零浪费。
	probe, err := p.probeInstance(ctx)
	if err != nil {
		_ = p.rt.Close(ctx)
		return nil, err
	}
	p.pool = newPool(p, probe, time.Duration(opts.IdleTTLMs)*time.Millisecond)
	return p, nil
}

// setupRuntime 挂 WASI、建宿主导入并编译二进制；失败即关 runtime，错误文案逐阶段固定。
func (p *Plugin) setupRuntime(ctx context.Context) error {
	if err := wasiInstantiate(ctx, p.rt); err != nil {
		_ = p.rt.Close(ctx)
		return fmt.Errorf("WASI 挂载失败: %w", err)
	}
	host, err := buildHostModule(ctx, p.rt, p.opts.Name, p.opts.Config, p.opts.Logger)
	if err != nil {
		_ = p.rt.Close(ctx)
		return fmt.Errorf("宿主导入构建失败: %w", err)
	}
	p.hostMod = host
	compiled, err := p.rt.CompileModule(ctx, p.opts.Binary)
	if err != nil {
		_ = p.rt.Close(ctx)
		return fmt.Errorf("非 wasm 或编译失败: %w", err)
	}
	p.compiled = compiled
	return nil
}

// probeInstance 建探测实例并校验 ABI 版本与 plugin_start；失败时实例与 runtime 均关闭，
// 调用方不得再复用 p.rt。
func (p *Plugin) probeInstance(ctx context.Context) (*instance, error) {
	probe, err := p.newInstance(ctx)
	if err != nil {
		_ = p.rt.Close(ctx)
		return nil, fmt.Errorf("实例化失败: %w", err)
	}
	if err := checkProbeVersion(ctx, probe); err != nil {
		_ = probe.mod.Close(ctx)
		_ = p.rt.Close(ctx)
		return nil, err
	}
	if err := checkProbeStart(ctx, probe); err != nil {
		_ = probe.mod.Close(ctx)
		_ = p.rt.Close(ctx)
		return nil, err
	}
	return probe, nil
}

// checkProbeVersion 校验 ABI 版本导出：无返回值或调用失败报「探测失败」，
// 有返回值但不等 ABIVersion 报「不匹配」并带双侧版本号。
func checkProbeVersion(ctx context.Context, probe *instance) error {
	ret, err := probe.version.Call(ctx)
	if err != nil || len(ret) != 1 {
		return fmt.Errorf("ABI 版本探测失败: %w", err)
	}
	if got := signed32(ret[0]); got != ABIVersion {
		return fmt.Errorf("ABI 版本不匹配: 插件 %d,宿主 %d", got, ABIVersion)
	}
	return nil
}

// checkProbeStart 校验 plugin_start 退出码；无返回值时按退出码 -1 上报。
func checkProbeStart(ctx context.Context, probe *instance) error {
	ret, err := probe.start.Call(ctx)
	if err != nil {
		return fmt.Errorf("plugin_start 调用失败: %w", err)
	}
	if len(ret) != 1 || signed32(ret[0]) != 0 {
		code := int64(-1)
		if len(ret) == 1 {
			code = int64(signed32(ret[0]))
		}
		return fmt.Errorf("plugin_start 失败(退出码 %d)", code)
	}
	return nil
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
		return p.settle(v, kind, detail)
	}
	p.pool.release(inst)
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

	// 入参 buffer 由宿主 malloc 后由宿主写入
	off, kind, detail := p.writeInput(ctx, inst, body)
	if kind != "" {
		return v, kind, detail
	}
	freeTimeout := time.Duration(p.opts.TimeoutMs) * time.Millisecond
	// 无论后续成败，入参与结果 buffer 都按同一分配器 free。
	hookRet, err := call1(ctx, inst.hook, offToU64(off), lenToU64(len(body)))
	if err != nil {
		_ = freeOff(ctx, inst, off, freeTimeout)
		k, d := classifyCallError(ctx, err)
		return v, k, d
	}
	verdict := signed32(uint64(hookRet))                           //nolint:gosec // wasm i32 返回值的位型回绕。
	if ferr := freeOff(ctx, inst, off, freeTimeout); ferr != nil { // 入参 buffer 用毕即还。
		// free 被 wazero 中断即模块已关闭，实例不可复用，按 containment 处置。
		k, d := classifyCallError(ctx, ferr)
		return v, k, d
	}
	if verdict == VerdictPass {
		v.Kind = VerdictKindPass
		return v, "", ""
	}
	if verdict != VerdictModified && verdict != VerdictReject {
		return v, ContainBadResult, fmt.Sprintf("verdict 非法: %d", verdict)
	}
	out, kind, detail := p.readResult(ctx, inst, off, freeTimeout)
	if kind != "" {
		return v, kind, detail
	}
	setVerdictBody(&v, verdict, out)
	return v, "", ""
}

// writeInput malloc 入参 buffer 并写入请求体，返回 offset；失败返回 containment 分类。
func (p *Plugin) writeInput(ctx context.Context, inst *instance, body []byte) (int64, ContainKind, string) {
	off, err := call1(ctx, inst.malloc, uint64(len(body)))
	if err != nil {
		k, d := classifyCallError(ctx, err)
		return 0, k, d
	}
	if off == 0 {
		return 0, ContainMalloc, "插件 malloc 返回 0(内存不足)"
	}
	if !inst.mod.Memory().Write(offToU32(off), body) {
		// 写已越界，free 失败不再改写结论。
		_ = freeOff(ctx, inst, off, time.Duration(p.opts.TimeoutMs)*time.Millisecond)
		return 0, ContainBadResult, fmt.Sprintf("入参写入越界(offset=%d len=%d)", off, len(body))
	}
	return off, "", ""
}

// readResult 读取 plugin_result 结果通道并释放其 buffer；inOff 为入参 buffer 起点，
// 结果原地复用时不再二次 free。
func (p *Plugin) readResult(
	ctx context.Context, inst *instance, inOff int64, freeTimeout time.Duration,
) ([]byte, ContainKind, string) {
	mem := inst.mod.Memory()
	res, err := call1(ctx, inst.resultFn)
	if err != nil {
		k, d := classifyCallError(ctx, err)
		return nil, k, d
	}
	resOff, resLen := unpackResult(res)
	if resLen < 0 || resLen > maxResultLen || resOff == 0 {
		return nil, ContainBadResult, fmt.Sprintf("结果通道非法(offset=%d len=%d)", resOff, resLen)
	}
	// 结果 buffer 复用入参 buffer 原地改写时只 free 一次。
	resOwnsBuffer := int64(resOff) != inOff
	buf, ok := mem.Read(offToU32(int64(resOff)), offToU32(int64(resLen)))
	if !ok {
		if resOwnsBuffer {
			_ = freeOff(ctx, inst, int64(resOff), freeTimeout)
		}
		return nil, ContainBadResult, fmt.Sprintf("结果读取越界(offset=%d len=%d)", resOff, resLen)
	}
	out := append([]byte(nil), buf...) // 拷出后立即释放插件内存。
	if !resOwnsBuffer {
		return out, "", ""
	}
	if ferr := freeOff(ctx, inst, int64(resOff), freeTimeout); ferr != nil {
		k, d := classifyCallError(ctx, ferr)
		return nil, k, d
	}
	return out, "", ""
}

// setVerdictBody 把结果通道内容写入结论：modified 携带新请求体，reject 携带截断理由。
func setVerdictBody(v *Verdict, verdict int32, out []byte) {
	if verdict == VerdictModified {
		v.Kind = VerdictKindModified
		v.Body = out
		return
	}
	v.Kind = VerdictKindRejected
	v.Reason = truncateReason(out)
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
	return signed64(ret[0]), nil
}

// wasm 线性偏移与 i32 返回值都以 uint64 承载 wasm 位型，下列辅助统一做位截断/回绕
// 换算，与直接类型转换的位语义一致，避免 gosec G115 在每处调用点报截断。
func signed32(v uint64) int32 { return int32(v) } //nolint:gosec // wasm i32 返回值的位型回绕，语义即要求截断。

func signed64(v uint64) int64 { return int64(v) } //nolint:gosec // wasm i64 返回值的位型回绕，语义即要求截断。

// offToU32 线性内存偏移按 wasm i32 位型回绕，越界由 Memory.Read/Write 返回 false 兜底。
func offToU32(off int64) uint32 { return uint32(off) } //nolint:gosec // 见上：位型回绕即契约。

// offToU64 同 offToU32，用于 wasm 参数传递。
func offToU64(off int64) uint64 { return uint64(off) } //nolint:gosec // 见 offToU32。

// lenToU64 长度转 wasm 参数；越界由插件侧 malloc 拒绝，宿主不做二次长度裁定。
func lenToU64(n int) uint64 { return uint64(n) } //nolint:gosec // 见上。

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
	_, err := inst.freeFn.Call(fctx, offToU64(off))
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
