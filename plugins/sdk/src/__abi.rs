//! ABI v0 机制层（内部模块，仅供 `plugin!` 宏展开转发，非公共 API）。
//!
//! 本模块是插件二进制中唯一允许出现 unsafe 的地方；安全 API 见 crate 根。
//! 三条不变量均来自真实故障，改动任何一条前先读 `docs/design/plugin-abi.md`：
//!
//! 1. **arena 必须在 `plugin_start` 一次性 `memory.grow` 预留**：否则 dlmalloc
//!    （Rust std 分配器，regex/serde 用）会从同一内存顶续页与 arena 重叠，
//!    宿主写入直接破坏 dlmalloc 堆元数据。预留后 dlmalloc 只能从预留区之后
//!    续页，双区与堆各安其位。
//! 2. **宿主 `free` 为显式 no-op，回收靠双区代际重置**：每次宿主 `malloc` 写入
//!    请求体即新请求，此刻重置 A/B 双区游标（A 区 = 宿主写入的请求体，
//!    B 区 = 钩子产物）。插件状态（规则/正则）驻留 dlmalloc 区，与 arena
//!    正交，天然不受重置影响。
//! 3. **scratch 静态缓冲三段错位**：key、配置值、日志共用同一缓冲，
//!    `get_config` 两段式与 `log` 各占一段，防止自读自写。
//!
//! 宿主侧行为对齐冻结契约（docs/design/plugin-abi.md §1/§3）：malloc 返回 0 =
//! 失败（0 号字节保留为失败哨兵）；请求体超过 A 区容量（25MiB）时 malloc 失败，
//! 交宿主按 fail 策略处置，不得越界写 B 区造成别名。

use std::sync::atomic::{AtomicI32, AtomicUsize, Ordering};

/// A 区容量（请求体侧）。预留总量 50MiB，默认 64MiB 实例上限下留 dlmalloc 余量。
pub(crate) const ARENA_A_CAP: usize = 25 << 20;
/// B 区容量（钩子产物侧）：改写产物可能大于原文，给同量级余量。
pub(crate) const ARENA_B_CAP: usize = 25 << 20;
/// 配置/日志静态缓冲容量。
const SCRATCH_BUF_CAP: usize = 64 << 10;
/// scratch 内 key 段上限，与配置值段从 256 起错位。
const SCRATCH_KEY_CAP: usize = 256;
/// 日志段基址偏移，与配置值段错开，防自读自写。
const SCRATCH_LOG_OFF: usize = 1280;
/// Reject 理由钳制：宿主契约超过 512 字节在宿主侧截断，SDK 先行钳制免送冗余字节。
const REJECT_REASON_CAP: usize = 512;

static ARENA_BASE: AtomicUsize = AtomicUsize::new(0);
static CURSOR_A: AtomicUsize = AtomicUsize::new(0);
static CURSOR_B: AtomicUsize = AtomicUsize::new(0);
/// 0 = 等待请求（host malloc 前）；1 = hook 期（结果走 B 区）。
static PHASE: AtomicI32 = AtomicI32::new(0);
static RES_OFF: AtomicUsize = AtomicUsize::new(0);
static RES_LEN: AtomicUsize = AtomicUsize::new(0);

static mut SCRATCH_BUF: [u8; SCRATCH_BUF_CAP] = [0u8; SCRATCH_BUF_CAP];

static SCRATCH_ADDR: AtomicUsize = AtomicUsize::new(0);

// ---- 平台层：wasm32 真实 FFI 与内存操作；宿主 stub 仅供编译，运行即 panic ----

#[cfg(target_arch = "wasm32")]
mod imp {
    pub(super) fn memory_size() -> usize {
        core::arch::wasm32::memory_size::<0>() as usize
    }

    pub(super) fn memory_grow(delta_pages: usize) -> usize {
        core::arch::wasm32::memory_grow(0, delta_pages)
    }

    pub(super) fn get_config(key_off: i32, key_len: i32, val_off: i32, val_len: i32) -> i32 {
        #[link(wasm_import_module = "muniu")]
        extern "C" {
            fn get_config(key_off: i32, key_len: i32, val_buf_off: i32, val_buf_len: i32) -> i32;
        }
        unsafe { get_config(key_off, key_len, val_off, val_len) }
    }

    pub(super) fn host_log(level: i32, offset: i32, len: i32) {
        #[link(wasm_import_module = "muniu")]
        extern "C" {
            #[link_name = "log"]
            fn log(level: i32, offset: i32, len: i32);
        }
        unsafe { log(level, offset, len) }
    }
}

#[cfg(not(target_arch = "wasm32"))]
mod imp {
    // 宿主 stub：仅令 SDK 在 x86_64 下可编译、可单测纯逻辑部分；
    // 这些路径只被 ABI 导出和宿主交互触发，wasm 运行时不可达。
    pub(super) fn memory_size() -> usize {
        unreachable!("仅 wasm32 目标可达")
    }
    pub(super) fn memory_grow(_delta_pages: usize) -> usize {
        unreachable!("仅 wasm32 目标可达")
    }
    pub(super) fn get_config(_key_off: i32, _key_len: i32, _val_off: i32, _val_len: i32) -> i32 {
        unreachable!("仅 wasm32 目标可达")
    }
    pub(super) fn host_log(_level: i32, _offset: i32, _len: i32) {
        unreachable!("仅 wasm32 目标可达")
    }
}

// ---- 线性内存与 scratch 原语（unsafe 收口） ----

/// 确保 [base, base+end_needed) 线性内存可用；不可增长（宿主内存上限）返回 false。
fn ensure(end_needed: usize) -> bool {
    loop {
        let cur = imp::memory_size() * 65536;
        if end_needed <= cur {
            return true;
        }
        let want = end_needed.div_ceil(65536) - cur.div_ceil(65536);
        if imp::memory_grow(want) == usize::MAX {
            return false;
        }
    }
}

fn scratch_addr() -> usize {
    let a = SCRATCH_ADDR.load(Ordering::Relaxed);
    if a != 0 {
        return a;
    }
    // SAFETY: 静态缓冲地址只取一次；写入一律经下方 raw 指针，不构成 static_mut 引用。
    let a = &raw const SCRATCH_BUF as usize;
    SCRATCH_ADDR.store(a, Ordering::Relaxed);
    a
}

/// 两段式读 per-plugin 配置（契约 §3）：首探 0 长缓冲取所需长度（-N），再取值。
/// 返回 None = key 不存在或超长（>63KiB-256，超出 scratch 容量）。
pub fn read_config(key: &str) -> Option<String> {
    let kb = key.as_bytes();
    if kb.len() + 1 > SCRATCH_KEY_CAP {
        return None;
    }
    let base = scratch_addr();
    let key_off = base;
    let val_off = base + SCRATCH_KEY_CAP;
    // SAFETY: key 写入 scratch key 段（容量已校验），与值段、日志段错位，无别名。
    unsafe {
        std::ptr::copy_nonoverlapping(kb.as_ptr(), key_off as *mut u8, kb.len());
    }
    // 首探传 0 长缓冲：值存在时宿主必返 -N（需要 N 字节），不会写入。
    let n = imp::get_config(key_off as i32, kb.len() as i32, val_off as i32, 0);
    let n = if n < 0 { (-n) as usize } else { return None };
    if n > SCRATCH_BUF_CAP - SCRATCH_KEY_CAP {
        return None;
    }
    let got = imp::get_config(key_off as i32, kb.len() as i32, val_off as i32, n as i32);
    if got != n as i32 {
        return None;
    }
    // SAFETY: 宿主刚写入 val 段 n 字节（n ≤ 容量），只读。
    let slice = unsafe { std::slice::from_raw_parts(val_off as *const u8, n) };
    Some(String::from_utf8_lossy(slice).into_owned())
}

/// 写宿主结构化日志（契约 §3）：字节拷入 scratch 日志段（与配置值段错位）后透传。
/// 超长截断到日志段容量。
pub fn write_log(level: i32, msg: &str) {
    let bytes = msg.as_bytes();
    let n = bytes.len().min(SCRATCH_BUF_CAP - SCRATCH_LOG_OFF);
    let addr = scratch_addr() + SCRATCH_LOG_OFF;
    // SAFETY: 日志段基址 = scratch + 1280，容量预留 64KiB-1280，与 key、值段错位。
    unsafe {
        std::ptr::copy_nonoverlapping(bytes.as_ptr(), addr as *mut u8, n);
    }
    imp::host_log(level, addr as i32, n as i32);
}

// ---- arena 与 ABI 导出实现（宏 shim 转发到这里） ----

/// `plugin_start` 第一步：arena 一次性预留（不变量 1）。失败返回 None（start 返回 3）。
pub fn reserve() -> Option<()> {
    let base = imp::memory_size() * 65536;
    ARENA_BASE.store(base, Ordering::Relaxed);
    let pages = (ARENA_A_CAP + ARENA_B_CAP).div_ceil(65536);
    if imp::memory_grow(pages) == usize::MAX {
        return None;
    }
    Some(())
}

/// `malloc`（契约 §1）：A 区 bump 分配；hook 期调用即新请求，双区游标代际重置（不变量 2）。
/// 返回 0 = 失败（负长度/超 A 区硬顶/不可续页），宿主按 fail 策略处置。
pub fn malloc(len: i32) -> i32 {
    if len < 0 {
        return 0;
    }
    if PHASE.load(Ordering::Relaxed) == 1 {
        CURSOR_A.store(0, Ordering::Relaxed);
        CURSOR_B.store(0, Ordering::Relaxed);
    }
    let len = len as usize;
    let base = ARENA_BASE.load(Ordering::Relaxed);
    let start = CURSOR_A.load(Ordering::Relaxed);
    if start + len > ARENA_A_CAP {
        // A 区硬顶（请求体 >25MiB）：契约行为为 malloc 失败，交宿主按 fail 策略处置，
        // 不得越界写入 B 区（钩子产物区）造成别名。
        return 0;
    }
    if !ensure(base + start + len) {
        return 0;
    }
    CURSOR_A.store(start + len, Ordering::Relaxed);
    PHASE.store(1, Ordering::Relaxed);
    (base + start) as i32
}

/// `free`（契约 §1）：代际回收下为显式 no-op，重复释放天然无害（不变量 2）。
pub fn free(_offset: i32) {}

/// 钩子入参视图（契约 §2）：宿主在本代际经 malloc 写入请求体后才调钩子，
/// buffer 在钩子返回前有效。插件侧只读；返回切片不得逃逸出当前钩子调用
/// （自由生命周期由调用方标注，编译器阻止其存入 `state` 等跨调用位置）。
pub fn input<'a>(offset: i32, len: i32) -> &'a [u8] {
    // SAFETY: offset/len 由宿主经本模块 malloc 分配并写入，钩子执行期内有效；
    // len 负值钳为 0（from_raw_parts 空切片合法）。
    unsafe { std::slice::from_raw_parts(offset as *const u8, len.max(0) as usize) }
}

/// B 区写入并挂结果通道：返回宿主可见 verdict（1=改写，2=拒绝）。
/// B 区不足（产物 >25MiB）时保守降级返回 0（放行原请求），不因插件资源问题阻断业务，
/// 宿主 containment 指标可观测该降级。
pub fn stage(data: &[u8], verdict: i32) -> i32 {
    let base = ARENA_BASE.load(Ordering::Relaxed);
    let start = CURSOR_B.load(Ordering::Relaxed);
    if start + data.len() > ARENA_B_CAP {
        return 0;
    }
    if !ensure(base + ARENA_A_CAP + start + data.len()) {
        return 0;
    }
    // SAFETY: B 区起点 = base + A 区容量，容量已校验，与 A 区、dlmalloc 区不重叠。
    unsafe {
        std::ptr::copy_nonoverlapping(
            data.as_ptr(),
            (base + ARENA_A_CAP + start) as *mut u8,
            data.len(),
        );
    }
    CURSOR_B.store(start + data.len(), Ordering::Relaxed);
    RES_OFF.store(base + ARENA_A_CAP + start, Ordering::Relaxed);
    RES_LEN.store(data.len(), Ordering::Relaxed);
    verdict
}

/// Reject 理由先行钳制：宿主契约截断至 512 字节，SDK 按字符边界先裁，免送冗余字节。
pub fn prepare_reason(reason: &str) -> &[u8] {
    let b = reason.as_bytes();
    let mut n = b.len().min(REJECT_REASON_CAP);
    while n > 0 && !reason.is_char_boundary(n) {
        n -= 1;
    }
    &b[..n]
}

/// `plugin_result`（契约 §1）：高 32 位 = 结果 offset，低 32 位 = 结果 len。
pub fn result_packed() -> i64 {
    let off = RES_OFF.load(Ordering::Relaxed) as i64;
    let len = RES_LEN.load(Ordering::Relaxed) as i64;
    (off << 32) | len
}
