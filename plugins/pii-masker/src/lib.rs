//! pii-masker:muniu 首发样例 WASM 插件(ABI v0,docs/design/plugin-abi.md)。
//!
//! 对入站请求载荷做正则 PII 脱敏:遍历 `messages[].content`(字符串或 content parts
//! 数组的 `text` 段)与 embeddings 的 `input`(字符串或字符串数组),命中规则即替换为
//! 占位符。规则经宿主 `get_config("rules")` 注入(JSON 数组
//! `[{"pattern":..., "replacement":...}]`),未配置时用内置缺省规则(身份证/手机号/邮箱);
//! 规则非法 → plugin_start 非 0,注册即拒载(坏规则不允许静默降级)。
//!
//! 内存策略(ABI 契约「插件不得假设两次调用间 buffer 留存」的代际利用):
//! - 宿主交换区双 arena 代际制:A 区放宿主写入的请求体(每次 `malloc` 即新请求,
//!   此刻重置双区游标),B 区放钩子产物;宿主 `free` 为显式 no-op(代际重置统一回收)。
//! - arena 在 plugin_start 一次性 `memory.grow` 预留(基址=当时内存顶);此后 dlmalloc
//!   (Rust std 分配器,regex/serde 用)只能从预留区之后续页——若不预留,dlmalloc 会
//!   从同一内存顶续页并与 arena 重叠,宿主写入直接打烂 dlmalloc 堆元数据(实证 OOB)。
//! - 请求体 >25MiB(A 区容量)时 malloc 返回 0,宿主按 fail 策略处置;
//!   memory_limit_mib 须 ≥ 50 + dlmalloc 余量,默认 64MiB 恰好覆盖。
//!
//! 构建:`./build.sh`(cargo --target wasm32-unknown-unknown);产物 `pii_masker.wasm`。

use regex::Regex;
use serde::Deserialize;
use std::sync::atomic::{AtomicI32, AtomicUsize, Ordering};
use std::sync::OnceLock;

// ---- 宿主导入(muniu 模块,ABI v0 §3)----

#[link(wasm_import_module = "muniu")]
extern "C" {
    fn get_config(key_off: i32, key_len: i32, val_buf_off: i32, val_buf_len: i32) -> i32;
    #[link_name = "log"]
    fn host_log(level: i32, offset: i32, len: i32);
}

// ---- 常量与全局状态(单线程 wasm,原子量只为满足 static 语义)----

/// 宿主交换区 A 区容量(请求体侧)。预留总量 50MiB,默认 64MiB 实例上限下留 dlmalloc 余量。
const ARENA_A_CAP: usize = 25 << 20;
/// B 区容量(钩子产物侧):改写产物可能大于原文,给同量级余量。
const ARENA_B_CAP: usize = 25 << 20;
/// 配置/日志静态缓冲容量。
const SCRATCH_BUF_CAP: usize = 64 << 10;

static ARENA_BASE: AtomicUsize = AtomicUsize::new(0);
static CURSOR_A: AtomicUsize = AtomicUsize::new(0);
static CURSOR_B: AtomicUsize = AtomicUsize::new(0);
/// 0 = 等待请求(host malloc 前);1 = hook 期(结果走 B 区)。
static PHASE: AtomicI32 = AtomicI32::new(0);
static RES_OFF: AtomicUsize = AtomicUsize::new(0);
static RES_LEN: AtomicUsize = AtomicUsize::new(0);

static mut SCRATCH_BUF: [u8; SCRATCH_BUF_CAP] = [0u8; SCRATCH_BUF_CAP];

static RULES: OnceLock<Vec<(Regex, String)>> = OnceLock::new();

#[derive(Deserialize)]
struct RuleDef {
    pattern: String,
    replacement: String,
}

// ---- ABI v0 导出 ----

#[no_mangle]
pub extern "C" fn muniu_abi_version() -> i32 {
    0
}

#[no_mangle]
pub extern "C" fn plugin_start() -> i32 {
    // arena 基址 = 当前线性内存顶,并立即整块预留:
    // 不预留则 dlmalloc 从同一顶续页与 arena 重叠,宿主写入会打烂 dlmalloc 堆元数据
    let base = memory_size() * 65536;
    ARENA_BASE.store(base, Ordering::Relaxed);
    let pages = (ARENA_A_CAP + ARENA_B_CAP).div_ceil(65536);
    if memory_grow(pages) == usize::MAX {
        return 3; // 预留失败:实例内存上限不足以承载交换区
    }

    let rules = match read_config("rules") {
        Some(json) => match parse_rules(&json) {
            Ok(r) => r,
            Err(e) => {
                host_log_str(3, &format!("pii-masker 规则非法: {e}"));
                return 1;
            }
        },
        None => default_rules(),
    };
    if RULES.set(rules).is_err() {
        return 2; // 重复 plugin_start(不应发生):实例已初始化
    }
    host_log_str(1, "pii-masker started");
    0
}

/// 宿主写入请求体的分配(每次调用即新请求:重置双区游标,代际回收)。
/// export_name 指定 ABI 导出名(函数本名 abi_* 避免与目标工具链可能的 C 运行时符号同名)。
#[export_name = "malloc"]
pub extern "C" fn abi_malloc(len: i32) -> i32 {
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
        return 0; // A 区硬顶(>25MiB 请求体):契约行为为 malloc 失败→宿主按 fail 策略处置,
    }             // 不得越界写入 B 区(钩子产物区)造成别名
    if !ensure(base + start + len) {
        return 0;
    }
    CURSOR_A.store(start + len, Ordering::Relaxed);
    PHASE.store(1, Ordering::Relaxed);
    (base + start) as i32
}

/// 代际回收:宿主按 ABI 调 free,arena 在下一请求 malloc 时整体重置,无需逐块释放。
#[export_name = "free"]
pub extern "C" fn abi_free(_offset: i32) {}

#[no_mangle]
pub extern "C" fn hook_request_body(offset: i32, len: i32) -> i32 {
    let rules = match RULES.get() {
        Some(r) if !r.is_empty() => r,
        _ => return 0, // 未初始化/空规则:放行(宿主已在钩子前完成 JSON 校验)
    };
    let input =
        unsafe { std::slice::from_raw_parts(offset as *const u8, len.max(0) as usize) };
    let mut value = match serde_json::from_slice::<serde_json::Value>(input) {
        Ok(v) => v,
        Err(_) => return 0, // 非 JSON(理论不可达,宿主已校验):放行
    };

    let mut modified = false;
    walk_value(&mut value, rules, &mut modified);
    if !modified {
        return 0;
    }
    let out = serde_json::to_vec(&value).unwrap_or_default();
    match arena_b_put(&out) {
        Some(off) => {
            RES_OFF.store(off, Ordering::Relaxed);
            RES_LEN.store(out.len(), Ordering::Relaxed);
            1
        }
        None => 0, // 产物区不足(宿主内存上限):保守放行原请求,不因脱敏资源问题阻断业务
    }
}

#[no_mangle]
pub extern "C" fn plugin_result() -> i64 {
    let off = RES_OFF.load(Ordering::Relaxed) as i64;
    let len = RES_LEN.load(Ordering::Relaxed) as i64;
    (off << 32) | len
}

// ---- 脱敏逻辑 ----

fn walk_value(v: &mut serde_json::Value, rules: &[(Regex, String)], modified: &mut bool) {
    match v {
        serde_json::Value::Object(map) => {
            for (k, val) in map.iter_mut() {
                match k.as_str() {
                    // chat:messages[].content 字符串,或 content parts 数组的 text 段(经 "text" 键)
                    "content" | "text" => {
                        if let serde_json::Value::String(s) = val {
                            *modified |= mask_str(s, rules);
                        }
                    }
                    // embeddings:input 字符串或字符串数组
                    "input" => match val {
                        serde_json::Value::String(s) => {
                            *modified |= mask_str(s, rules);
                        }
                        serde_json::Value::Array(items) => {
                            for item in items.iter_mut() {
                                if let serde_json::Value::String(s) = item {
                                    *modified |= mask_str(s, rules);
                                }
                            }
                        }
                        _ => {}
                    },
                    _ => {}
                }
                walk_value(val, rules, modified);
            }
        }
        serde_json::Value::Array(items) => {
            for item in items.iter_mut() {
                walk_value(item, rules, modified);
            }
        }
        _ => {}
    }
}

fn mask_str(s: &mut String, rules: &[(Regex, String)]) -> bool {
    let mut changed = false;
    for (re, repl) in rules {
        if re.is_match(s) {
            // NoExpand:占位符不做 $ 组引用展开
            *s = re.replace_all(s, regex::NoExpand(repl)).into_owned();
            changed = true;
        }
    }
    changed
}

fn parse_rules(json: &str) -> Result<Vec<(Regex, String)>, String> {
    let defs: Vec<RuleDef> =
        serde_json::from_str(json).map_err(|e| format!("规则 JSON 解析失败: {e}"))?;
    let mut out = Vec::with_capacity(defs.len());
    for d in defs {
        let re = Regex::new(&d.pattern).map_err(|e| format!("正则非法 {}: {e}", d.pattern))?;
        out.push((re, d.replacement));
    }
    Ok(out)
}

/// 内置缺省规则:身份证 18 位 → 手机号 11 位 → 邮箱(先长后短,防短模式截断长串)。
fn default_rules() -> Vec<(Regex, String)> {
    let defs = [
        (
            r"\b[1-9]\d{5}(?:19|20)\d{2}(?:0[1-9]|1[0-2])(?:[0-2]\d|3[01])\d{3}[\dXx]\b",
            "[ID]",
        ),
        (r"\b1[3-9]\d{9}\b", "[PHONE]"),
        (r"[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}", "[EMAIL]"),
    ];
    defs.iter()
        .filter_map(|(p, r)| Regex::new(p).ok().map(|re| (re, r.to_string())))
        .collect()
}

// ---- 配置读取(get_config 两段式:0 缓冲探长度,再取值)----

fn read_config(key: &str) -> Option<String> {
    let kb = key.as_bytes();
    if kb.len() + 1 > 256 {
        return None;
    }
    let base = scratch_addr();
    let key_off = base;
    let val_off = base + 256;
    unsafe {
        std::ptr::copy_nonoverlapping(kb.as_ptr(), key_off as *mut u8, kb.len());
    }
    // 首探传 0 长缓冲:值存在时宿主必返 -N(需要 N 字节),不会写入
    let n = unsafe { get_config(key_off as i32, kb.len() as i32, val_off as i32, 0) };
    let n = if n < 0 { (-n) as usize } else { return None };
    if n > SCRATCH_BUF_CAP - 256 {
        return None;
    }
    let got = unsafe { get_config(key_off as i32, kb.len() as i32, val_off as i32, n as i32) };
    if got != n as i32 {
        return None;
    }
    let slice = unsafe { std::slice::from_raw_parts((val_off) as *const u8, n) };
    Some(String::from_utf8_lossy(slice).into_owned())
}

fn host_log_str(level: i32, msg: &str) {
    let bytes = msg.as_bytes();
    let n = bytes.len().min(SCRATCH_BUF_CAP - 1280);
    let addr = scratch_addr() + 1280; // 与配置值缓冲(256 起)错开,避免自读自写
    unsafe {
        std::ptr::copy_nonoverlapping(bytes.as_ptr(), addr as *mut u8, n);
        host_log(level, addr as i32, n as i32);
    }
}

static SCRATCH_ADDR: AtomicUsize = AtomicUsize::new(0);

fn scratch_addr() -> usize {
    let a = SCRATCH_ADDR.load(Ordering::Relaxed);
    if a != 0 {
        return a;
    }
    let a = &raw const SCRATCH_BUF as usize;
    SCRATCH_ADDR.store(a, Ordering::Relaxed);
    a
}

// ---- arena B(钩子产物区)与线性内存确保 ----

fn arena_b_put(data: &[u8]) -> Option<usize> {
    let base = ARENA_BASE.load(Ordering::Relaxed);
    let start = CURSOR_B.load(Ordering::Relaxed);
    if start + data.len() > ARENA_B_CAP {
        return None;
    }
    if !ensure(base + ARENA_A_CAP + start + data.len()) {
        return None;
    }
    unsafe {
        std::ptr::copy_nonoverlapping(data.as_ptr(), (base + ARENA_A_CAP + start) as *mut u8, data.len());
    }
    CURSOR_B.store(start + data.len(), Ordering::Relaxed);
    Some(base + ARENA_A_CAP + start)
}

/// 确保 [base, base+end_needed) 线性内存可用;不可增长(宿主内存上限)返回 false。
fn ensure(end_needed: usize) -> bool {
    loop {
        let cur = memory_size() * 65536;
        if end_needed <= cur {
            return true;
        }
        let want = end_needed.div_ceil(65536) - cur.div_ceil(65536);
        if memory_grow(want) == usize::MAX {
            return false;
        }
    }
}

#[inline]
fn memory_size() -> usize {
    core::arch::wasm32::memory_size::<0>() as usize
}

#[inline]
fn memory_grow(delta_pages: usize) -> usize {
    core::arch::wasm32::memory_grow(0, delta_pages)
}
