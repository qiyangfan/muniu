//! pii-masker:muniu 样例 WASM 插件(ABI v0,docs/design/plugin-abi.md)。
//!
//! 对入站请求载荷做正则 PII 脱敏:遍历 `messages[].content`(字符串或 content parts
//! 数组的 `text` 段)与 embeddings 的 `input`(字符串或字符串数组),命中规则即替换为
//! 占位符。规则经宿主 `get_config("rules")` 注入(JSON 数组
//! `[{"pattern":..., "replacement":...}]`),未配置时用内置缺省规则(身份证/手机号/邮箱);
//! 规则非法 → init Err → 注册即拒载(坏规则不允许静默降级)。
//!
//! ABI 导出、双 arena 代际回收、两段式配置读取等机制全部由 muniu-plugin-sdk 承接
//! (本文件零 unsafe);构建:`plugins/build.sh`,产物 `pii_masker.wasm`。

use muniu_plugin_sdk as sdk;
use regex::Regex;
use serde::Deserialize;

#[derive(Deserialize)]
struct RuleDef {
    pattern: String,
    replacement: String,
}

/// 编译后的规则集(init 产物,驻留 SDK 状态区,跨调用存活)。
#[derive(Debug)]
struct Rules(Vec<(Regex, String)>);

// 宿主单测构建下 ABI 导出由 SDK 宏 cfg 出局,入口函数需 dead_code 豁免
#[cfg_attr(not(target_arch = "wasm32"), allow(dead_code))]
fn init(config: &sdk::Config) -> Result<Rules, String> {
    let rules = match config.json::<Vec<RuleDef>>("rules")? {
        Some(defs) => compile_rules(defs)?,
        None => Rules(default_rules()),
    };
    sdk::log(sdk::Level::Info, "pii-masker started");
    Ok(rules)
}

#[cfg_attr(not(target_arch = "wasm32"), allow(dead_code))]
fn on_request_body(body: &[u8], rules: &Rules) -> sdk::Verdict {
    // 非 JSON 理论不可达(宿主已在钩子前完成校验):保守放行
    let Ok(mut value) = serde_json::from_slice::<serde_json::Value>(body) else {
        return sdk::Verdict::Pass;
    };
    let mut modified = false;
    walk_value(&mut value, &rules.0, &mut modified);
    if !modified {
        return sdk::Verdict::Pass;
    }
    match serde_json::to_vec(&value) {
        Ok(out) => sdk::Verdict::Rewrite(out),
        Err(_) => sdk::Verdict::Pass, // 序列化失败(不应发生):放行原请求
    }
}

muniu_plugin_sdk::plugin! {
    state: Rules,
    init: init,
    hook_request_body: on_request_body,
}

// ---- 脱敏逻辑(纯函数,宿主可单测) ----

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
        if let Some(next) = replace_bounded(s, re, repl) {
            *s = next;
            changed = true;
        }
    }
    changed
}

/// ASCII 词边界校验的替换引擎(2026-09-13 用户裁决的行为修复):
/// 命中两侧不得紧邻 ASCII 字母数字——保住「不啃长串」的防截断性质(如手机号规则
/// 不会咬进身份证数字串);CJK/标点/空白均视为边界——号码紧贴中文可命中。
/// 不用 `\b` 锚点的原因:regex crate 的 `\b` 走 Unicode `\w` 语义,CJK 与数字同为
/// 词字符,锚点方案在「电话139…」这类最高频中文形态上整体失明,且 crate 无
/// lookaround 可补救;边界判定上收到引擎层是唯一同时满足两个性质的解。
/// 占位符按字面拼接(等价恒久 NoExpand),永不做 $ 组引用展开。
fn replace_bounded(text: &str, re: &Regex, repl: &str) -> Option<String> {
    let hits: Vec<_> = re
        .find_iter(text)
        .filter(|m| ascii_bounded(text, m.start(), m.end()))
        .collect();
    if hits.is_empty() {
        return None;
    }
    let mut out = String::with_capacity(text.len());
    let mut last = 0;
    for m in hits {
        out.push_str(&text[last..m.start()]);
        out.push_str(repl);
        last = m.end();
    }
    out.push_str(&text[last..]);
    Some(out)
}

/// 命中区间 [start, end) 两侧是否为 ASCII 词边界(串端或非 ASCII 字母数字)。
/// 字节级检查安全:正则命中边界必为 UTF-8 字符边界,多字节续字节非 ASCII。
fn ascii_bounded(text: &str, start: usize, end: usize) -> bool {
    let b = text.as_bytes();
    let before = start == 0 || !b[start - 1].is_ascii_alphanumeric();
    let after = end == b.len() || !b[end].is_ascii_alphanumeric();
    before && after
}

fn compile_rules(defs: Vec<RuleDef>) -> Result<Rules, String> {
    let mut out = Vec::with_capacity(defs.len());
    for d in defs {
        let re = Regex::new(&d.pattern).map_err(|e| format!("正则非法 {}: {e}", d.pattern))?;
        out.push((re, d.replacement));
    }
    Ok(Rules(out))
}

/// 内置缺省规则:身份证 18 位 → 手机号 11 位 → 邮箱(先长后短,防短模式截断长串)。
/// 模式不带 `\b`:词边界由引擎层 ascii_bounded 统一施加(见 mask_str 的裁决注释)。
fn default_rules() -> Vec<(Regex, String)> {
    let defs = [
        (
            r"[1-9]\d{5}(?:19|20)\d{2}(?:0[1-9]|1[0-2])(?:[0-2]\d|3[01])\d{3}[\dXx]",
            "[ID]",
        ),
        (r"1[3-9]\d{9}", "[PHONE]"),
        (r"[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}", "[EMAIL]"),
    ];
    defs.iter()
        .filter_map(|(p, r)| Regex::new(p).ok().map(|re| (re, r.to_string())))
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    fn rules() -> Vec<(Regex, String)> {
        default_rules()
    }

    #[test]
    fn walk_命中_chat_content_与_parts_text() {
        let mut v = json!({
            "model": "qwen-plus",
            "messages": [
                {"role": "user", "content": "联系 13812345678"},
                {"role": "user", "content": [
                    {"type": "text", "text": "邮箱 a@b.com"},
                    {"type": "image_url", "image_url": {"url": "https://x/1.png"}}
                ]}
            ]
        });
        let mut modified = false;
        walk_value(&mut v, &rules(), &mut modified);
        assert!(modified);
        assert_eq!(
            v["messages"][0]["content"],
            json!("联系 [PHONE]"),
            "字符串 content 应被脱敏"
        );
        assert_eq!(
            v["messages"][1]["content"][0]["text"],
            json!("邮箱 [EMAIL]"),
            "parts 的 text 段应被脱敏"
        );
        assert_eq!(
            v["messages"][1]["content"][1]["image_url"]["url"],
            json!("https://x/1.png"),
            "非文本字段不得误伤"
        );
    }

    #[test]
    fn walk_命中_embeddings_input_字符串与数组() {
        let mut v = json!({
            "input": ["身份证 110101199003077512", "无事"]
        });
        let mut modified = false;
        walk_value(&mut v, &rules(), &mut modified);
        assert!(modified);
        assert_eq!(v["input"][0], json!("身份证 [ID]"));
        assert_eq!(v["input"][1], json!("无事"));
    }

    #[test]
    fn walk_未命中_零改动() {
        let mut v = json!({"messages": [{"role": "user", "content": "你好"}]});
        let mut modified = false;
        walk_value(&mut v, &rules(), &mut modified);
        assert!(!modified, "无命中必须零改动(宿主以 modified 决定 verdict)");
    }

    #[test]
    fn mask_紧贴中文命中_修复unicode词边界缺陷() {
        // 2026-09-13 用户裁决:\b 走 Unicode \w 语义致「电话139…」整体失明是缺陷,
        // 引擎层 ASCII 词边界修复——紧贴中文必须命中(最高频中文形态)
        let mut s = String::from("证件11010119900307551X邮件a@b.com电话13912345678");
        assert!(mask_str(&mut s, &rules()));
        assert_eq!(s, "证件[ID]邮件[EMAIL]电话[PHONE]");
    }

    #[test]
    fn mask_紧邻ascii字母数字拒判_保守防截断() {
        // 边界的另一半:紧邻 ASCII 字母数字 = 命中在更长 token 内部,保守不动
        let mut glued_alnum = String::from("x13912345678");
        assert!(!mask_str(&mut glued_alnum, &rules()));
        assert_eq!(
            glued_alnum, "x13912345678",
            "前邻 ASCII 字母:疑似更长 token,拒绝"
        );

        let mut id_in_run = String::from("911010119900307551X");
        assert!(!mask_str(&mut id_in_run, &rules()));
        assert_eq!(id_in_run, "911010119900307551X", "身份证前邻数字:拒绝截断");

        // 行为变化记录:无边界自定义模式的「长 token 内部子串」命中由旧引擎的可命中
        // 收敛为拒判;如确需子串语义,未来经配置显式加 boundary 开关(暂无消费者)
        let custom = vec![(Regex::new(r"\d{4}").unwrap(), "[N]".to_string())];
        let mut s = String::from("ab1234cd");
        assert!(!mask_str(&mut s, &custom));
        assert_eq!(s, "ab1234cd", "自定义无边界模式同样受引擎边界约束");

        let mut spaced = String::from("ab 1234 cd");
        assert!(mask_str(&mut spaced, &custom));
        assert_eq!(spaced, "ab [N] cd");
    }

    #[test]
    fn mask_替换序_长规则先跑_短规则不截断长串() {
        // 无 \b 边界的构造规则,使顺序真正承重:短规则先跑会把 20 位串啃成残渣
        let rs = vec![
            (Regex::new(r"\d{18}").unwrap(), "[LONG]".to_string()),
            (Regex::new(r"\d{6}").unwrap(), "[SHORT]".to_string()),
        ];
        let mut s = String::from("编号199012345678901234");
        assert!(mask_str(&mut s, &rs));
        assert_eq!(s, "编号[LONG]");
    }

    #[test]
    #[allow(non_snake_case)]
    fn mask_NoExpand_占位符不做组引用展开() {
        let rs = vec![(Regex::new(r"\d+").unwrap(), "[$0]".to_string())];
        let mut s = String::from("n=42");
        assert!(mask_str(&mut s, &rs));
        assert_eq!(s, "n=[$0]", "$0 必须按字面输出,不得展开为捕获组");
    }

    #[test]
    fn compile_rules_非法正则_报错定位_pattern() {
        let err = compile_rules(vec![RuleDef {
            pattern: "((".into(),
            replacement: "x".into(),
        }])
        .unwrap_err();
        assert!(err.contains("正则非法 (("), "错误信息应定位 pattern: {err}");
    }

    #[test]
    fn compile_rules_自定义规则生效() {
        let rs = compile_rules(vec![RuleDef {
            pattern: r"\b4111\d{12}\b".into(),
            replacement: "[CARD]".into(),
        }])
        .unwrap();
        let mut s = String::from("卡号 4111111111111111");
        assert!(mask_str(&mut s, &rs.0));
        assert_eq!(s, "卡号 [CARD]");
    }
}
