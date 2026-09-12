// Package plugin 实现 WASM 插件运行时(ADR-0011):wazero 宿主、自定薄 ABI v0
// (docs/design/plugin-abi.md 冻结)。依赖方向:仅依赖 protocol/metrics 等基建叶子包,
// 被 proxy 消费,不反向依赖任何业务包。
//
// 隔离契约:插件崩溃/超时/超内存只损失该请求与该实例;禁用时主路径零开销
// (Registry 为 nil 时 proxy 直接短路,不进本包任何代码)。
package plugin

import "time"

// ABIVersion 当前宿主支持的 ABI 版本(v0,docs/design/plugin-abi.md §1)。
const ABIVersion = 0

// ABI 契约常量(冻结,变更走 ADR)。
const (
	// HostModuleName 宿主导入模块名(插件 import "muniu.*")。
	HostModuleName = "muniu"

	// ExportVersion ABI 版本探测导出:()->i32,必须返回 ABIVersion。
	ExportVersion = "muniu_abi_version"
	// ExportStart 实例初始化导出:()->i32,0=成功。
	ExportStart = "plugin_start"
	// ExportMalloc 分配导出:(len i32)->offset i32,0=失败。
	ExportMalloc = "malloc"
	// ExportFree 释放导出:(offset i32)。
	ExportFree = "free"
	// ExportHook 请求体钩子导出:(offset, len i32)->verdict i32。
	ExportHook = "hook_request_body"
	// ExportResult 结果通道导出:()->i64,高 32 位 offset、低 32 位 len。
	ExportResult = "plugin_result"

	// ExportMemory 线性内存导出名(插件必须导出)。
	ExportMemory = "memory"
)

// hook_request_body 的 verdict 码(冻结)。
const (
	VerdictPass     = 0 // 放行,未改写
	VerdictModified = 1 // 改写:plugin_result() 给出新请求体
	VerdictReject   = 2 // 拒绝:plugin_result() 给出理由文本
)

// 宿主导入 muniu.log 的 level 码。
const (
	LogDebug = 0
	LogInfo  = 1
	LogWarn  = 2
	LogError = 3
)

// RejectReasonLimit 拒绝理由宿主侧截断上限(字节,plugin-abi.md §1)。
const RejectReasonLimit = 512

// ResourceDefaults 资源限制默认值(plugin-abi.md §5;全部可按插件配置)。
const (
	DefaultMemoryLimitPages = 1024 // 64 MiB(每页 64 KiB)
	DefaultTimeoutMs        = 50   // 单次调用超时
	DefaultPoolSize         = 8    // 每插件实例池
	DefaultIdleTTLMs        = 60_000
	MaxIdleTTLMs            = 24 * 60 * 60 * 1000 // 空闲回收 TTL 硬顶 24h
	MaxTimeoutMs            = 10_000              // 单次调用超时硬顶(与 config 层 plugins 校验同值)
	MaxMemoryLimitPages     = 4096                // 256 MiB 配置硬顶,防误配拖垮宿主(超限 wazero 会 panic,必须在入口钳制)
	MaxPoolSize             = 64
	// DefaultProbeTimeoutMs 注册探测(实例化/版本/start)超时下限:plugin_start 可能
	// 做重初始化(如编译正则),按调用超时的倍数给足,但不允许注册链无界挂死。
	DefaultProbeTimeoutMs = 2_000
	MaxProbeTimeoutMs     = 60_000
)

// ContainKind containment 事件分类(指标 label 与审计日志共用)。
type ContainKind string

// containment 分类取值(见 runtime.go classifyCallError)。
const (
	ContainTimeout   ContainKind = "timeout"    // 调用超时(epoch 中断)
	ContainTrap      ContainKind = "trap"       // 插件 trap(unreachable/OOB 等)
	ContainOOM       ContainKind = "oom"        // 超实例内存上限
	ContainMalloc    ContainKind = "malloc"     // 插件 malloc 失败(返回 0)
	ContainExit      ContainKind = "exit"       // 插件主动 proc_exit
	ContainInstance  ContainKind = "instance"   // 实例创建失败(start 非 0 等)
	ContainBadResult ContainKind = "bad_result" // 结果通道非法(offset/len 越界)
)

// VerdictKind 处置结论类型。
type VerdictKind int

// 处置结论取值。
const (
	VerdictKindPass VerdictKind = iota
	VerdictKindModified
	VerdictKindRejected
	VerdictKindFailed // containment:调用失败(fail 策略由调用方按插件配置处置)
)

// Verdict 单次调用结论(单插件一次调用)。
type Verdict struct {
	Kind    VerdictKind
	Body    []byte    // Kind=Modified:改写后请求体
	Reason  string    // Kind=Rejected:拒绝理由(已截断);Kind=Failed:containment 摘要
	Plugin  string    // 触发插件名(Reject/Failed 时填充)
	started time.Time // 调用起点(耗时观测;包内使用)
}

// unpackResult:i64 结果通道解码(高 32 位 offset、低 32 位 len;编码见 plugin-abi.md §1)。
func unpackResult(v int64) (offset, length int32) {
	return int32(v >> 32), int32(v & 0xffffffff)
}
