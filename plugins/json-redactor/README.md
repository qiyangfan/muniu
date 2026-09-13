# json-redactor — muniu 样例 WASM 插件(ABI v0)

JSON Pointer **结构定向**脱敏:按指针精确路径定位请求体中的字段,执行删除或整值
替换。与 pii-masker 的「内容扫描」正交——自由文本里的号码交给正则,已结构化的
字段(如 `metadata.user_id`、`messages[*].name`)交给指针。基于 `muniu-plugin-sdk`
实现(业务代码 ~120 行,零 unsafe),同时充当「SDK 钩子抽象不绑定单一玩法」的
第二证明。ABI 契约见 `docs/design/plugin-abi.md`。

## 规则

宿主配置注入(`plugins[].config.rules` 或管理面注册参数),JSON 数组:

```json
[
  {"pointer": "/messages/*/name", "op": "remove"},
  {"pointer": "/metadata/user_id", "op": "replace", "value": "[REDACTED]"}
]
```

- **指针语法**:RFC 6901 JSON Pointer(`~1` → `/`、`~0` → `~`),另支持 `*` 通配段
  (匹配数组任意下标或对象任意键)。
- **操作**:`remove` 删字段/下标;`replace` 整值替换——只覆盖**既有**字段,不新增。
- **不可解路径静默跳过**(键不存在/数组越界/类型不匹配):结构定向的本性是
  「目标字段存在才动手」;零命中零改写(宿主按 verdict=0 放行,不留痕头)。
- **对象上的 `*` 通配会清/换全部键**——引导只对数组使用;对象建议逐键精确指定。
- 配置非法(指针不以 `/` 开头、`~` 转义非法、op 非 remove/replace、replace 缺
  value)→ init Err → 注册即拒载,与 pii-masker 坏规则同纪律。

## 构建

```bash
rustup target add wasm32-unknown-unknown   # 首次
../build.sh                                # workspace 全量构建,产物 ./json_redactor.wasm
# 或仓库根:make plugin-build
```

## 使用(muniu 侧)

静态注册(muniu.yaml):

```yaml
plugins:
  - name: json-redactor
    path: ./plugins/json-redactor/json_redactor.wasm
    config:
      rules: |
        [{"pointer":"/messages/*/name","op":"remove"},
         {"pointer":"/metadata/user_id","op":"replace","value":"[REDACTED]"}]
```

运行态注册/启停/热替换与 pii-masker 相同(管理 API,见 `plugins/pii-masker/README.md`)。

## 测试

- Rust 宿主单测:`cd plugins && cargo test -p json-redactor`(指针转义、通配、
  嵌套、remove/replace、miss 跳过、校验负面)。
- 宿主视角真工件回归:`go test -race ./internal/plugin/ -run TestRealArtifact`(工件
  存在时自动生效)。
