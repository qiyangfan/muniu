//! json-redactor:muniu 样例 WASM 插件(ABI v0,docs/design/plugin-abi.md)。
//!
//! JSON Pointer 字段级脱敏——与 pii-masker 的「内容扫描」正交的「结构定向」机制:
//! 按 RFC 6901 指针(`~1`→`/`、`~0`→`~`)+ `*` 通配段(匹配数组任意下标或对象
//! 任意键)定位目标值,执行 `remove`(删字段)或 `replace`(整值替换)。规则经宿主
//! `get_config("rules")` 注入(JSON 数组):
//!
//! ```json
//! [{"pointer": "/messages/*/name", "op": "remove"},
//!  {"pointer": "/metadata/user_id", "op": "replace", "value": "[REDACTED]"}]
//! ```
//!
//! 语义约定:中途不可解(键不存在/数组越界/类型不匹配)= 该路径静默跳过——结构
//! 定向的本性是「目标字段存在才动手」;`modified` 只记真实发生的改动,零命中零改写。
//! 配置非法(指针不以 `/` 开头、op 非 remove/replace、replace 缺 value、`~` 转义
//! 非法)→ init Err → 注册即拒载,与 pii-masker 坏规则同纪律。
//!
//! ABI 导出与内存策略由 muniu-plugin-sdk 承接(本文件零 unsafe);构建:
//! `plugins/build.sh`,产物 `json_redactor.wasm`。

use muniu_plugin_sdk as sdk;
use serde::Deserialize;
use serde_json::Value;

#[derive(Deserialize)]
struct RuleDef {
    pointer: String,
    op: String,
    value: Option<Value>,
}

/// 编译后的规则(init 产物,驻留 SDK 状态区,跨调用存活)。
struct Rules(Vec<Rule>);

struct Rule {
    segs: Vec<Seg>,
    op: Op,
}

enum Op {
    Remove,
    Replace(Value),
}

/// 指针段:对象键 / 数组下标(运行期按容器类型解释)/ `*` 通配。
#[derive(Debug, PartialEq)]
enum Seg {
    Token(String),
    Any,
}

// 宿主单测构建下 ABI 导出由 SDK 宏 cfg 出局,入口函数需 dead_code 豁免
#[cfg_attr(not(target_arch = "wasm32"), allow(dead_code))]
fn init(config: &sdk::Config) -> Result<Rules, String> {
    let defs: Vec<RuleDef> = config
        .json("rules")?
        .ok_or_else(|| "缺少 rules 配置".to_string())?;
    let rules = compile_rules(defs)?;
    sdk::log(sdk::Level::Info, "json-redactor started");
    Ok(rules)
}

#[cfg_attr(not(target_arch = "wasm32"), allow(dead_code))]
fn on_request_body(body: &[u8], rules: &Rules) -> sdk::Verdict {
    // 非 JSON 理论不可达(宿主已在钩子前完成校验):保守放行
    let Ok(mut value) = serde_json::from_slice::<Value>(body) else {
        return sdk::Verdict::Pass;
    };
    let mut modified = false;
    for rule in &rules.0 {
        apply_rule(&mut value, &rule.segs, &rule.op, &mut modified);
    }
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

// ---- 指针解析与树操作(纯函数,宿主可单测) ----

/// RFC 6901 指针解析:`/a~1b/* /c` → [Token("a/b"), Any, Token("c")]。
/// `~` 后必须跟 0 或 1,其余为非法指针。
fn parse_pointer(pointer: &str) -> Result<Vec<Seg>, String> {
    if !pointer.starts_with('/') {
        return Err(format!("指针非法 {pointer:?}:必须以 / 开头"));
    }
    let mut segs = Vec::new();
    for raw in pointer[1..].split('/') {
        if raw == "*" {
            segs.push(Seg::Any);
            continue;
        }
        let mut token = String::with_capacity(raw.len());
        let mut chars = raw.chars();
        while let Some(c) = chars.next() {
            if c != '~' {
                token.push(c);
                continue;
            }
            match chars.next() {
                Some('0') => token.push('~'),
                Some('1') => token.push('/'),
                other => {
                    return Err(format!(
                        "指针非法 {pointer:?}:~ 转义须为 ~0/~1,实为 {other:?}"
                    ))
                }
            }
        }
        segs.push(Seg::Token(token));
    }
    Ok(segs)
}

fn compile_rules(defs: Vec<RuleDef>) -> Result<Rules, String> {
    let mut out = Vec::with_capacity(defs.len());
    for d in defs {
        let segs = parse_pointer(&d.pointer)?;
        let op = match d.op.as_str() {
            "remove" => Op::Remove,
            "replace" => Op::Replace(
                d.value
                    .ok_or_else(|| format!("规则非法 {}:replace 操作必须提供 value", d.pointer))?,
            ),
            other => {
                return Err(format!(
                    "规则非法 {}:op 须为 remove|replace,实为 {other:?}",
                    d.pointer
                ))
            }
        };
        out.push(Rule { segs, op });
    }
    Ok(Rules(out))
}

/// RFC 6901 数组下标解析:仅接受规范十进制(无前导零、无符号、非空)。
/// 裸 `str::parse` 会把 "01"/"+1" 洗成合法下标——RFC 语义下二者不命名任何数组
/// 元素,放行会让规则命中契约上不存在的目标(评审实钉,2026-09-13)。
fn parse_array_index(token: &str) -> Option<usize> {
    if token.is_empty() || (token.len() > 1 && token.starts_with('0')) {
        return None;
    }
    if !token.bytes().all(|b| b.is_ascii_digit()) {
        return None;
    }
    token.parse().ok()
}

/// 递归应用单条规则;不可解路径静默跳过,真实改动才置 modified。
/// 前缀段逐层下行,最后一段以容器视角执行操作(remove 需要键名/下标所属容器)。
fn apply_rule(v: &mut Value, segs: &[Seg], op: &Op, modified: &mut bool) {
    let Some(seg) = segs.first() else {
        return; // 空指针已在编译期拒绝,防御
    };
    let rest = &segs[1..];
    if rest.is_empty() {
        apply_on_container(v, seg, op, modified);
        return;
    }
    match (seg, v) {
        (Seg::Any, Value::Array(items)) => {
            for item in items.iter_mut() {
                apply_rule(item, rest, op, modified);
            }
        }
        (Seg::Any, Value::Object(map)) => {
            for (_k, val) in map.iter_mut() {
                apply_rule(val, rest, op, modified);
            }
        }
        (Seg::Token(key), Value::Object(map)) => {
            if let Some(val) = map.get_mut(key) {
                apply_rule(val, rest, op, modified);
            } // 键不存在:静默跳过
        }
        (Seg::Token(key), Value::Array(items)) => {
            if let Some(idx) = parse_array_index(key) {
                if let Some(val) = items.get_mut(idx) {
                    apply_rule(val, rest, op, modified);
                } // 越界:跳过
            } // 数组容器遇非数字/非规范 token:类型不匹配,跳过
        }
        _ => {} // 容器类型不匹配(如对字符串取段):跳过
    }
}

/// 最后一段的容器视角操作:键/下标存在才动手(replace 只覆盖既有字段,不新增)。
/// `*` 通配最后一段 = 对容器的全部成员执行操作(对象通配会清/换全部键,README 明示)。
fn apply_on_container(container: &mut Value, seg: &Seg, op: &Op, modified: &mut bool) {
    match (container, seg) {
        (Value::Object(map), Seg::Token(key)) => match op {
            Op::Remove => {
                *modified |= map.remove(key).is_some();
            }
            Op::Replace(new) => {
                if let Some(slot) = map.get_mut(key) {
                    *slot = new.clone();
                    *modified = true;
                }
            }
        },
        (Value::Array(items), Seg::Token(key)) => {
            let Some(idx) = parse_array_index(key) else {
                return;
            };
            if idx < items.len() {
                match op {
                    Op::Remove => {
                        items.remove(idx);
                        *modified = true;
                    }
                    Op::Replace(new) => {
                        items[idx] = new.clone();
                        *modified = true;
                    }
                }
            }
        }
        (Value::Array(items), Seg::Any) => match op {
            Op::Remove => {
                *modified |= !items.is_empty();
                items.clear();
            }
            Op::Replace(new) => {
                for item in items.iter_mut() {
                    *item = new.clone();
                }
                *modified |= !items.is_empty();
            }
        },
        (Value::Object(map), Seg::Any) => match op {
            Op::Remove => {
                *modified |= !map.is_empty();
                map.clear();
            }
            Op::Replace(new) => {
                for (_k, val) in map.iter_mut() {
                    *val = new.clone();
                }
                *modified |= !map.is_empty();
            }
        },
        _ => {} // 容器类型与段不匹配:跳过
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    fn rule(pointer: &str, op: &str, value: Option<Value>) -> Rule {
        compile_rules(vec![RuleDef {
            pointer: pointer.into(),
            op: op.into(),
            value,
        }])
        .unwrap()
        .0
        .remove(0)
    }

    fn apply(mut v: Value, r: &Rule) -> (Value, bool) {
        let mut modified = false;
        apply_rule(&mut v, &r.segs, &r.op, &mut modified);
        (v, modified)
    }

    #[test]
    fn pointer_rfc6901_转义() {
        assert_eq!(
            parse_pointer("/a~1b/c~0d").unwrap(),
            vec![Seg::Token("a/b".into()), Seg::Token("c~d".into())]
        );
        assert_eq!(
            parse_pointer("/x/*").unwrap(),
            vec![Seg::Token("x".into()), Seg::Any]
        );
    }

    #[test]
    fn pointer_负面_不以斜杠开头_坏转义() {
        assert!(parse_pointer("messages/name").is_err());
        assert!(parse_pointer("/a~2b").is_err(), "~2 不是合法 RFC 6901 转义");
        assert!(parse_pointer("/a~").is_err());
    }

    #[test]
    fn compile_负面_op非法_replace缺value() {
        let defs = |d: RuleDef| compile_rules(vec![d]).is_err();
        assert!(defs(RuleDef {
            pointer: "/a".into(),
            op: "drop".into(),
            value: None
        }));
        assert!(defs(RuleDef {
            pointer: "/a".into(),
            op: "replace".into(),
            value: None
        }));
        assert!(!defs(RuleDef {
            pointer: "/a".into(),
            op: "replace".into(),
            value: Some(json!("[R]")),
        }));
    }

    #[test]
    fn remove_对象字段() {
        let v = json!({"messages": [{"role": "user", "name": "张三", "content": "hi"}]});
        let (out, modified) = apply(v, &rule("/messages/0/name", "remove", None));
        assert!(modified);
        assert!(out["messages"][0].get("name").is_none());
        assert_eq!(
            out["messages"][0]["content"],
            json!("hi"),
            "兄弟字段不得误伤"
        );
    }

    #[test]
    fn replace_仅覆盖既有字段_不新增() {
        let v = json!({"metadata": {"user_id": "u-1"}});
        let (out, modified) = apply(
            v,
            &rule("/metadata/user_id", "replace", Some(json!("[REDACTED]"))),
        );
        assert!(modified);
        assert_eq!(out["metadata"]["user_id"], json!("[REDACTED]"));

        let v = json!({"metadata": {}});
        let (out, modified) = apply(
            v,
            &rule("/metadata/user_id", "replace", Some(json!("[REDACTED]"))),
        );
        assert!(!modified, "字段不存在时 replace 不得新增");
        assert!(out["metadata"].get("user_id").is_none());
    }

    #[test]
    fn 通配_数组任意下标() {
        let v = json!({"messages": [
            {"role": "user", "name": "张三"},
            {"role": "assistant"},
            {"role": "user", "name": "李四"}
        ]});
        let (out, modified) = apply(v, &rule("/messages/*/name", "remove", None));
        assert!(modified);
        assert!(out["messages"][0].get("name").is_none());
        assert!(
            out["messages"][1].get("name").is_none(),
            "无该键的元素静默跳过"
        );
        assert!(out["messages"][2].get("name").is_none());
    }

    #[test]
    fn 通配_对象任意键_明示清空语义() {
        let v = json!({"metadata": {"a": 1, "b": "x"}});
        let (out, modified) = apply(v, &rule("/metadata/*", "remove", None));
        assert!(modified);
        assert_eq!(out["metadata"], json!({}));
    }

    #[test]
    fn miss与类型不匹配_静默跳过() {
        let v = json!({"messages": [{"content": "文本"}, "非对象元素"]});
        // 指向不存在键
        let (out, modified) = apply(v.clone(), &rule("/messages/0/name", "remove", None));
        assert!(!modified);
        assert_eq!(out, v, "零命中零改写");
        // 对字符串取段(类型不匹配)
        let (out, modified) = apply(v.clone(), &rule("/messages/1/name", "remove", None));
        assert!(!modified);
        assert_eq!(out, v);
        // 数组越界
        let (out, modified) = apply(v.clone(), &rule("/messages/9/name", "remove", None));
        assert!(!modified);
        assert_eq!(out, v);
    }

    #[test]
    fn 数组下标_remove_移位与_replace_覆盖() {
        let v = json!({"tags": ["a", "b", "c"]});
        let (out, modified) = apply(v, &rule("/tags/1", "remove", None));
        assert!(modified);
        assert_eq!(out["tags"], json!(["a", "c"]));

        let v = json!({"tags": ["a", "b", "c"]});
        let (out, modified) = apply(v, &rule("/tags/2", "replace", Some(json!("X"))));
        assert!(modified);
        assert_eq!(out["tags"], json!(["a", "b", "X"]));
    }

    #[test]
    fn 数组下标_非规范形式不命中_rfc6901() {
        // RFC 6901:数组下标须为规范十进制;"01"/"+1" 不命名任何元素,
        // 裸 parse 会把它们洗成 1 而误伤(评审修复钉子)
        for bad in ["/tags/01", "/tags/+1", "/tags/ 1", "/tags/-1", "/tags/"] {
            let v = json!({"tags": ["a", "b"]});
            let (out, modified) = apply(v, &rule(bad, "remove", None));
            assert!(!modified, "{bad} 不得命中");
            assert_eq!(out["tags"], json!(["a", "b"]), "{bad} 不得改动数组");
        }
        assert_eq!(parse_array_index("0"), Some(0), "0 是合法下标");
        assert_eq!(parse_array_index("10"), Some(10));
    }

    #[test]
    fn 多规则_顺序应用() {
        let rules = compile_rules(vec![
            RuleDef {
                pointer: "/messages/*/name".into(),
                op: "remove".into(),
                value: None,
            },
            RuleDef {
                pointer: "/user".into(),
                op: "replace".into(),
                value: Some(json!("[REDACTED]")),
            },
        ])
        .unwrap();
        let mut v = json!({"messages": [{"name": "张三"}], "user": "u-9"});
        let mut modified = false;
        for r in &rules.0 {
            apply_rule(&mut v, &r.segs, &r.op, &mut modified);
        }
        assert!(modified);
        assert!(v["messages"][0].get("name").is_none());
        assert_eq!(v["user"], json!("[REDACTED]"));
    }

    #[test]
    fn 顶层即数组_路径可用() {
        let v = json!([{"name": "张三"}, {"name": "李四"}]);
        let (out, modified) = apply(v, &rule("/*/name", "remove", None));
        assert!(modified);
        assert_eq!(out, json!([{}, {}]));
    }
}
