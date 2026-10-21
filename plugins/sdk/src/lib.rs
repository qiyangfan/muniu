//! muniu-plugin-sdk 为 WASM 插件提供 ABI v0 的配置、日志和请求体钩子接口。
//! 插件作者只需实现初始化和请求处理函数，并在 crate 根声明：
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
//! // 初始化并读取插件配置。
//! fn init(config: &sdk::Config) -> Result<State, String> {
//!     let cfg: GreetConfig = config
//!         .json("rules")?
//!         .ok_or_else(|| "缺少 rules 配置".to_string())?;
//!     sdk::log(sdk::Level::Info, "greet-plugin started");
//!     Ok(State { greeting: cfg.greeting })
//! }
//!
//! // 处理请求体并返回放行、改写或拒绝。
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
//! 请求体改写必须返回完整合法 JSON；钩子返回的请求体切片不得跨调用保存。
//! ABI 细节见 `docs/design/plugin-abi.md`。

use serde::de::DeserializeOwned;

/// 仅供 `plugin!` 宏展开转发;符号公开是宏跨 crate 展开的技术需要,非公共 API。
#[doc(hidden)]
pub mod __abi;

/// 只读的宿主配置句柄。
pub struct Config;

impl Config {
    /// 读取配置字符串；未配置或超出缓冲区时返回 None。
    pub fn raw(&self, key: &str) -> Option<String> {
        __abi::read_config(key)
    }

    /// 读取配置并将其解析为 JSON 类型 `T`。
    pub fn json<T: DeserializeOwned>(&self, key: &str) -> Result<Option<T>, String> {
        match self.raw(key) {
            None => Ok(None),
            Some(s) => parse_config_value(key, &s).map(Some),
        }
    }
}

/// 解析配置 JSON，并在错误中包含配置键名。
fn parse_config_value<T: DeserializeOwned>(key: &str, raw: &str) -> Result<T, String> {
    serde_json::from_str(raw).map_err(|e| format!("配置 {key} JSON 解析失败: {e}"))
}

/// 宿主日志级别。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Level {
    Debug = 0,
    Info = 1,
    Warn = 2,
    Error = 3,
}

/// 向宿主写入结构化日志。
pub fn log(level: Level, msg: &str) {
    __abi::write_log(level as i32, msg);
}

/// 请求钩子的三种处理结果。
pub enum Verdict {
    /// 放行原请求。
    Pass,
    /// 以完整合法 JSON 请求体替换原请求。
    Rewrite(Vec<u8>),
    /// 拒绝请求并返回原因。
    Reject(String),
}

/// 在 crate 根声明插件入口，并生成 ABI v0 所需的导出。
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
