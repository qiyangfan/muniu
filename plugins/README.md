# plugins/ — muniu WASM 插件工作区(Cargo workspace)

muniu 的样例插件与插件 SDK。ABI v0 契约(冻结)见 `docs/design/plugin-abi.md`,
宿主实现见 `internal/plugin`,选型与教训见 ADR-0011。

## 成员

| crate | 类型 | 说明 |
|---|---|---|
| `sdk`(muniu-plugin-sdk) | 库 | ABI v0 机制层:6 导出生成、双 arena 代际回收、两段式配置读取、日志透传;**unsafe 全部收口于此**,插件作者零 unsafe |
| `pii-masker` | 插件 | 内容扫描:正则规则脱敏自由文本(身份证/手机号/邮箱,可自定义) |
| `json-redactor` | 插件 | 结构定向:JSON Pointer 字段级删除/替换(与内容扫描正交的定位机制) |

## 构建

```bash
rustup target add wasm32-unknown-unknown   # 首次
./build.sh                                 # = cargo build --release --target wasm32-unknown-unknown
# 或仓库根:make plugin-build(cargo 缺席时优雅跳过)
```

产物落位到各插件目录(文档与 muniu.yaml 样例引用的稳定路径):

| 产物 | 注册路径 |
|---|---|
| `pii-masker/pii_masker.wasm` | `./plugins/pii-masker/pii_masker.wasm` |
| `json-redactor/json_redactor.wasm` | `./plugins/json-redactor/json_redactor.wasm` |

CI:`.github/workflows/plugins.yml`(plugins/** 触发:workspace 构建 + Rust 宿主单测 + artifact 上传)。

## 写一个新插件(SDK 快速上手)

新建目录,`Cargo.toml` 依赖 `muniu-plugin-sdk`(path = "../sdk")+ 加入 workspace members,
`src/lib.rs` 只需两个函数加一行声明(完整示例即 `json-redactor`):

```rust
use muniu_plugin_sdk as sdk;
use serde::Deserialize;

#[derive(Deserialize)]
struct MyRule { pattern: String }

struct MyState { /* init 产物,跨调用存活 */ }

fn init(config: &sdk::Config) -> Result<MyState, String> {
    // config.json::<T>("key")? 读宿主配置(两段式 get_config 已封装);
    // Err → plugin_start 非 0 → 注册拒载(坏配置不允许静默降级)
    todo!()
}

fn on_body(body: &[u8], state: &MyState) -> sdk::Verdict {
    // body = 完整请求体(宿主已校验合法 JSON);返回 Pass / Rewrite(完整 JSON 字节) / Reject(理由)
    todo!()
}

muniu_plugin_sdk::plugin! {
    state: MyState,
    init: init,
    hook_request_body: on_body,
}
```

约定(详见 SDK 文档与 ABI 契约):

- 目标 `wasm32-unknown-unknown`(零 WASI 导入,沙箱面最小);构建产物复制到插件目录。
- 词边界由 SDK 无关的插件语义决定——pii-masker 采用引擎层 ASCII 词边界(紧贴中文命中、
  长 ASCII token 内部拒判),新插件按自身语义取舍。
- 插件**不得持有跨调用的内存指针**(ABI §2):状态一律走 `state`(SDK 代管,
  驻留 dlmalloc 区,与 arena 代际回收正交)。
- 内存预算:A/B 双区各 25MiB,默认 64MiB 实例上限覆盖 ≈25MiB 请求体;改写产物超 B 区
  保守降级放行。

## Rust 侧测试与门禁

```bash
cd plugins && cargo test     # 宿主单测:插件纯逻辑 + SDK 可测纯函数(cfg 双态,见 sdk/src/__abi.rs)
cargo clippy --all-targets -- -D warnings
cargo fmt --check
```

宿主视角的真插件回归(本地工件存在时自动生效):

```bash
go test -race ./internal/plugin/ -run TestRealArtifact -v
```
