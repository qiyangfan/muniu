//! json-redactor 是 muniu 的 WASM 示例插件，按 JSON Pointer 定位并修改字段。
//! 指针遵循 RFC 6901，支持 `*` 匹配任意对象键或数组下标；规则由宿主通过 `rules` 配置传入：
//!
//! ```json
//! [{"pointer": "/messages/*/name", "op": "remove"},
//!  {"pointer": "/metadata/user_id", "op": "replace", "value": "[REDACTED]"}]
//! ```
//!
//! 不存在、不匹配或越界的路径会跳过；非法规则会使插件初始化失败。

use muniu_plugin_sdk as sdk;
use serde::Deserialize;
use serde_json::Value;

#[derive(Deserialize)]
struct RuleDef {
    pointer: String,
    op: String,
    value: Option<Value>,
}

/// 初始化时编译的规则集。
struct Rules(Vec<Rule>);

struct Rule {
    segs: Vec<Seg>,
    op: Op,
}

enum Op {
    Remove,
    Replace(Value),
}

/// 指针中的对象键、数组下标或通配段。
#[derive(Debug, PartialEq)]
enum Seg {
    Token(String),
    Any,
}

// 非 wasm 构建不生成 ABI 导出，入口函数仅由宏在 wasm 目标中引用。
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
    // 宿主已校验请求体；解析失败时保留原请求。
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

// ---- JSON Pointer 解析与树操作 ----

/// 解析 RFC 6901 指针，并将 `~0`、`~1` 解码为 `~`、`/`。
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

/// 只接受规范十进制数组下标，不接受前导零或正号。
fn parse_array_index(token: &str) -> Option<usize> {
    if token.is_empty() || (token.len() > 1 && token.starts_with('0')) {
        return None;
    }
    if !token.bytes().all(|b| b.is_ascii_digit()) {
        return None;
    }
    token.parse().ok()
}

/// 递归应用一条规则；目标路径不存在时跳过，实际修改时设置 modified。
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

/// 对路径最后一段执行操作；replace 只修改已存在的值。
/// 最后一段为 `*` 时操作容器中的全部成员。
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
        // 裸 parse 会将 "01" 或 "+1" 解析为下标 1，导致误删字段。
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
