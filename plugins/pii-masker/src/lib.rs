//! pii-masker 是 muniu 的 WASM 示例插件，用正则替换请求中的身份证号、手机号和邮箱。
//! 规则可通过宿主配置提供；未配置时使用内置规则，非法规则会使插件初始化失败。

use muniu_plugin_sdk as sdk;
use regex::Regex;
use serde::Deserialize;

#[derive(Deserialize)]
struct RuleDef {
    pattern: String,
    replacement: String,
}

/// 初始化时编译的规则集。
#[derive(Debug)]
struct Rules(Vec<(Regex, String)>);

// 非 wasm 构建不生成 ABI 导出，入口函数仅由宏在 wasm 目标中引用。
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
    // 宿主已校验请求体；解析失败时保留原请求。
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

// ---- 脱敏逻辑 ----

fn walk_value(v: &mut serde_json::Value, rules: &[(Regex, String)], modified: &mut bool) {
    match v {
        serde_json::Value::Object(map) => {
            for (k, val) in map.iter_mut() {
                match k.as_str() {
                    // chat 消息正文和 content part 的 text。
                    "content" | "text" => {
                        if let serde_json::Value::String(s) = val {
                            *modified |= mask_str(s, rules);
                        }
                    }
                    // embeddings 的 input。
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

/// 只替换两侧不紧邻 ASCII 字母或数字的匹配项，中文字符可作为边界。
/// 不使用 `\b`，因为正则引擎会将中文视为词字符；替换文本按字面量拼接。
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

/// 检查匹配区间两侧是否不是 ASCII 字母或数字。
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

/// 返回内置的身份证号、手机号和邮箱规则，较长号码先匹配。
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
        // ASCII 词边界允许手机号紧贴中文命中。
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

        // 词边界同样约束自定义规则:长 token 内部的子串一律不命中;
        // 如确需子串语义,应新增显式的 boundary 配置开关
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
