//! muniu-plugin-sdk — muniu WASM 插件 SDK(ABI v0,docs/design/plugin-abi.md)。
//!
//! 把 ABI 机制(导出符号、线性内存交换、双 arena 代际回收、两段式配置读取、
//! scratch 错位)全部收口在 crate 内部 unsafe 模块(`__abi`),插件作者零 unsafe,
//! 只写两个函数并在 crate 根声明:
//!
//! ```no_run
//! use muniu_plugin_sdk as sdk;
//! use serde::Deserialize;
//!
//! #[derive(Deserialize)]
//! struct GreetConfig {
//!     greeting: String,
//! }
//!
//! struct State {
//!     greeting: String,
//! }
//!
//! // init:读宿主配置、编译规则;Err → plugin_start 非 0,注册即拒载(坏配置不允许静默降级)
//! fn init(config: &sdk::Config) -> Result<State, String> {
//!     let cfg: GreetConfig = config
//!         .json("rules")?
//!         .ok_or_else(|| "缺少 rules 配置".to_string())?;
//!     sdk::log(sdk::Level::Info, "greet-plugin started");
//!     Ok(State { greeting: cfg.greeting })
//! }
//!
//! // 钩子:入参 = 完整请求体字节(宿主已保证合法 JSON);返回三态
//! fn on_body(body: &[u8], state: &State) -> sdk::Verdict {
//!     let _ = state;
//!     if body.is_empty() {
//!         return sdk::Verdict::Pass;
//!     }
//!     // ...检查/改写/拒绝...
//!     sdk::Verdict::Pass
//! }
//!
//! muniu_plugin_sdk::plugin! {
//!     state: State,
//!     init: init,
//!     hook_request_body: on_body,
//! }
//! ```
//!
//! 行为契约(与 ABI v0 冻结文档一一对应,宿主侧零感知):
//! - `plugin_start` 退出码:0 成功;1 init Err(拒载);2 重复启动(防御);3 arena 预留失败。
//! - 钩子 verdict:`Pass`→0 放行;`Rewrite`→1 改写(产物经 B 区 + 结果通道交宿主,
//!   产物必须是合法 JSON,宿主会重新执行入站解析);`Reject`→2 拒绝(理由经结果通道,
//!   宿主截断至 512 字节,SDK 已先行按字符边界钳制)。
//! - 产物/理由超过 B 区容量(25MiB)时保守降级为放行(0),不因插件资源问题阻断业务。
//! - 目标 `wasm32-unknown-unknown`(零 WASI 导入,沙箱面最小);宿主 `free` 为
//!   no-op,回收靠代际重置——**插件不得持有跨调用内存指针**(ABI §2),
//!   状态一律驻留 `state`(init 产物,dlmalloc 区,与 arena 正交)。

use serde::de::DeserializeOwned;

/// 仅供 `plugin!` 宏展开转发;符号公开是宏跨 crate 展开的技术需要,非公共 API。
#[doc(hidden)]
pub mod __abi;

/// 宿主配置只读句柄(`plugin_start` 期注入 init;契约 §3:字符串直通,v0 不做结构校验)。
pub struct Config;

impl Config {
    /// 读配置原始字符串。None = key 不存在或值超过 SDK 缓冲容量(64KiB)。
    pub fn raw(&self, key: &str) -> Option<String> {
        __abi::read_config(key)
    }

    /// 读配置并按 JSON 解析为 `T`。解析失败 = Err(建议直接让 init 返回 Err 拒载)。
    pub fn json<T: DeserializeOwned>(&self, key: &str) -> Result<Option<T>, String> {
        match self.raw(key) {
            None => Ok(None),
            Some(s) => parse_config_value(key, &s).map(Some),
        }
    }
}

/// 配置 JSON 解析(独立纯函数便于宿主单测):错误信息带 key 定位。
fn parse_config_value<T: DeserializeOwned>(key: &str, raw: &str) -> Result<T, String> {
    serde_json::from_str(raw).map_err(|e| format!("配置 {key} JSON 解析失败: {e}"))
}

/// 宿主日志级别(契约 §3)。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Level {
    Debug = 0,
    Info = 1,
    Warn = 2,
    Error = 3,
}

/// 写宿主结构化日志(经宿主透传,自动带插件名标签);超长截断到 SDK 缓冲容量。
pub fn log(level: Level, msg: &str) {
    __abi::write_log(level as i32, msg);
}

/// 钩子裁决三态(ABI v0 钩子语义全量)。
pub enum Verdict {
    /// 放行:请求体原样继续(verdict 0)。
    Pass,
    /// 改写:产物为**完整合法 JSON** 请求体字节,宿主重新执行入站解析(verdict 1)。
    Rewrite(Vec<u8>),
    /// 拒绝:理由经结果通道交宿主,超出 512 字节按字符边界钳制(verdict 2)。
    Reject(String),
}

/// 声明插件:在 crate 根生成 ABI v0 全部 6 个导出,并接管内存策略与生命周期。
///
/// 错误码契约见 crate 根文档;`malloc`/`free` 采用 ABI 导出名(规避与工具链
/// C 运行时符号同名),`hook_request_body` 期宿主再次 malloc 即代际重置。
///
/// - `state`:init 成功产物类型,每次钩子以 `&state` 注入;
/// - `init`:`fn(&Config) -> Result<S, String>`,Err 即拒载(plugin_start = 1);
/// - `hook_request_body`:`fn(&[u8], &S) -> Verdict`。
#[macro_export]
macro_rules! plugin {
    (state: $state:ty, init: $init:path, hook_request_body: $hook:path $(,)?) => {
        // 宏卫生:用户路径在 crate 根解析为别名,生成模块内只引用别名
        //(`__muniu_` 前缀为 SDK 保留名);type 别名同理,规避 ty 片段无法加路径前缀
        #[doc(hidden)]
        #[allow(dead_code)]
        type __muniu_state_ty = $state;
        #[doc(hidden)]
        #[allow(unused_imports)]
        use $hook as __muniu_hook_fn;
        #[doc(hidden)]
        #[allow(unused_imports)]
        use $init as __muniu_init_fn;

        #[cfg(target_arch = "wasm32")]
        mod __muniu_plugin_exports {
            use super::__muniu_hook_fn;
            use super::__muniu_init_fn;
            use super::__muniu_state_ty;

            static __STATE: std::sync::OnceLock<__muniu_state_ty> = std::sync::OnceLock::new();

            #[no_mangle]
            pub extern "C" fn muniu_abi_version() -> i32 {
                0
            }

            #[no_mangle]
            pub extern "C" fn plugin_start() -> i32 {
                // arena 预留失败:实例内存上限不足以承载交换区
                let Some(()) = $crate::__abi::reserve() else {
                    return 3;
                };
                match __muniu_init_fn(&$crate::Config) {
                    Ok(state) => {
                        if __STATE.set(state).is_err() {
                            return 2; // 重复 plugin_start(不应发生):实例已初始化
                        }
                        0
                    }
                    Err(e) => {
                        $crate::log($crate::Level::Error, &format!("plugin_start 拒载: {e}"));
                        1
                    }
                }
            }

            /// 宿主写入请求体的分配;export_name 指定 ABI 导出名(函数本名 abi_*
            /// 避免与目标工具链可能的 C 运行时符号同名)。
            #[export_name = "malloc"]
            pub extern "C" fn abi_malloc(len: i32) -> i32 {
                $crate::__abi::malloc(len)
            }

            /// 代际回收:宿主按 ABI 调 free,arena 在下一请求 malloc 时整体重置。
            #[export_name = "free"]
            pub extern "C" fn abi_free(offset: i32) {
                $crate::__abi::free(offset)
            }

            #[no_mangle]
            pub extern "C" fn hook_request_body(offset: i32, len: i32) -> i32 {
                let Some(state) = __STATE.get() else {
                    return 0; // 未初始化(不应发生):放行,宿主已在钩子前完成 JSON 校验
                };
                match __muniu_hook_fn($crate::__abi::input(offset, len), state) {
                    $crate::Verdict::Pass => 0,
                    $crate::Verdict::Rewrite(body) => $crate::__abi::stage(&body, 1),
                    $crate::Verdict::Reject(reason) => {
                        $crate::__abi::stage($crate::__abi::prepare_reason(&reason), 2)
                    }
                }
            }

            #[no_mangle]
            pub extern "C" fn plugin_result() -> i64 {
                $crate::__abi::result_packed()
            }
        }
    };
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde::Deserialize;

    #[derive(Deserialize, PartialEq, Debug)]
    struct RuleDef {
        pattern: String,
    }

    #[test]
    fn parse_config_value_合法_json() {
        let v: Vec<RuleDef> =
            parse_config_value("rules", r#"[{"pattern":"1[3-9]\\d{9}"}]"#).unwrap();
        assert_eq!(v.len(), 1);
        assert_eq!(v[0].pattern, r"1[3-9]\d{9}");
    }

    #[test]
    fn parse_config_value_非法_json_错误带_key() {
        let err = parse_config_value::<Vec<RuleDef>>("rules", "not-json").unwrap_err();
        assert!(err.contains("配置 rules"), "错误信息应定位 key: {err}");
    }

    #[test]
    fn parse_config_value_形态不符_报错() {
        let err = parse_config_value::<RuleDef>("rules", r#"[{"pattern":"x"}]"#).unwrap_err();
        assert!(err.contains("配置 rules"), "错误信息应定位 key: {err}");
    }

    #[test]
    fn level_数值对齐_abi_契约() {
        assert_eq!(Level::Debug as i32, 0);
        assert_eq!(Level::Info as i32, 1);
        assert_eq!(Level::Warn as i32, 2);
        assert_eq!(Level::Error as i32, 3);
    }
}
