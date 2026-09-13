# Design — plugin-rust-sdk

## 决策 1:`plugin!` 声明宏在插件 crate 内生成 6 导出,SDK crate 不直接放 `#[no_mangle]`

ABI v0 的 6 个导出(`muniu_abi_version`/`plugin_start`/`malloc`/`free`/`hook_request_body`/`plugin_result`)由 SDK 提供的 `plugin!` 宏在**插件 crate 根**展开生成,内部转发到 SDK 的 `__abi` 模块。

- 为什么不把导出函数直接写在 SDK crate 里:wasm 链接器(rust-lld `--gc-sections`)对依赖 crate 中未被根 crate 引用的符号有回收风险,导出是否存活取决于工具链行为——不可赌。宏展开落在最终 cdylib 根 crate,导出生效由语言语义保证。
- 先例:proxy-wasm Rust SDK 的 `main!`、extism-pdk 的插件宏,同一问题域同一解法。
- 宏形态(显式状态类型,不搞类型推断魔法):

```rust
muniu_plugin_sdk::plugin! {
    state: Rules,                 // init 成功后驻留的状态类型
    init: init,                   // fn(&Config) -> Result<Rules, String>;Err → plugin_start=1 拒载
    hook_request_body: on_body,   // fn(&[u8], &Rules) -> Verdict
}
```

- `Verdict` 三态覆盖 ABI 钩子全语义:`Pass`(verdict 0)/`Rewrite(Vec<u8>)`(verdict 1,产物走 B 区 + 结果通道)/`Reject(String)`(verdict 2,理由走结果通道,宿主截断 512B)。首发两插件虽只用 Pass/Rewrite,但 SDK 是「ABI 机制层」,拒绝路径是契约的一半,缺失它 SDK 就没抽象完整。

## 决策 2:unsafe 收口边界——单一 `abi` 模块 + `cfg(target_arch)` 双态,宿主单测可行

- SDK 全部 unsafe(线性内存指针构造、arena 游标推进、scratch 错位)收口在 `src/abi.rs` 一个模块,模块头注释固化三条不变量(全部来自已实证的坑,原文迁移):
  1. arena 必须在 `plugin_start` 一次性 `memory.grow` 预留——否则 dlmalloc 从同一内存顶续页与 arena 重叠,宿主写入打烂堆元数据(实证 OOB containment);
  2. 宿主 `free` 为显式 no-op,回收靠下一请求 `malloc` 时的双区代际重置——插件不得持有跨调用指针(ABI §2 的代际利用);
  3. scratch 静态缓冲内 key(0-256)/val(256+)/log(+1280)三段错位——`get_config`/`log` 复用同一缓冲时防自读自写。
- `core::arch::wasm32` 内建函数在 x86_64 宿主不存在,`abi` 模块按 `target_arch` 分双态:wasm32 侧为真实 FFI 与内存操作;宿主侧同签名 stub(`unreachable!`,文档注明仅 wasm 目标可达)。宏生成的导出模块整体 `#[cfg(target_arch = "wasm32")]`。效果:`cargo test`(宿主)可编译运行插件纯逻辑与 SDK 可测纯函数,`cargo build --target wasm32-unknown-unknown` 编译全量——宿主门禁与 wasm 构建互不拖累。

## 决策 3:plugin_start 错误码映射保持原语义(宿主可见行为零变化)

| 退出码 | 语义 | 来源 |
|---|---|---|
| 0 | 初始化成功 | — |
| 1 | init 返回 Err(坏配置/坏规则) | 拒载,不允许静默降级(与现行一致) |
| 2 | 重复 plugin_start | 实例已初始化,防御 |
| 3 | arena 预留失败(内存上限不足) | 实例不可用 |

宿主对非 0 一律拒载/弃实例,码值语义是给日志与排障的;映射原样保留,重构前后注册行为可逐码对照(回归钉子覆盖码 1)。

## 决策 4:json-redactor 语义——RFC 6901 JSON Pointer + `*` 通配段,remove/replace 双操作

与 pii-masker 的机制正交性(证明 SDK 抽象普适的关键):

| | pii-masker | json-redactor |
|---|---|---|
| 定位方式 | 内容扫描(正则命中即替换) | 结构定向(JSON Pointer 精确路径) |
| 输入抽象 | 字符串值逐个过规则 | 树上定位后整值操作 |
| 典型场景 | 自由文本里的身份证/手机号/邮箱 | 已结构化字段(`metadata.user_id`、`messages/*/name`) |

- 配置(`plugins[].config.rules`,JSON 数组):`[{"pointer": "/messages/*/name", "op": "remove"}, {"pointer": "/metadata/user_id", "op": "replace", "value": "[REDACTED]"}]`。
- Pointer 解析按 RFC 6901(`~1`→`/`、`~0`→`~`);扩展:`*` 段匹配数组的任意下标或对象的任意键(对象通配在 README 明示风险,引导只对数组用)。
- 中途不可解(类型不匹配/键不存在)= 该路径静默跳过——结构定向的本性是「目标字段存在才动手」,与 pii-masker 的「命中即改」语义不同;modified 只记真实发生的改动。
- 校验错误(指针不以 `/` 开头、`replace` 缺 `value`)→ init Err → 拒载,与 pii-masker 坏规则同纪律。

## 决策 5:无 spec delta(skip_specs: true)的论证

SDK 是插件侧构建期工具,`plugin-runtime` spec 的全部需求(注册校验/钩子挂点/containment/性能护栏)描述宿主行为,本次零变化;ABI v0 契约冻结文字零变化;json-redactor 是样例资产,与 pii-masker 同层,主 spec 从未为样例插件立需求。不为过 validate 而发明需求。

## 决策 6:Go 侧真工件回归钉子——skip-if-absent,不进 CI 强依赖

新增 `internal/plugin` 集成测试:若 `plugins/pii-masker/pii_masker.wasm` 存在,则经真实 wazero 运行时加载(SDK 重构产物),钉住注册探测/启动/改写 verdict/结果通道/坏配置拒载(码 1)全链;工件缺席 `t.Skip`。

- 为什么值得:本次变更的全部风险 = 「SDK 重构是否保持 ABI 行为」,自动钉子优于纯手工冒烟,且让后续每次插件重构都有本地回归门。
- 为什么 skip-if-absent 而非入库产物:仓库现状 wasm 产物 gitignore(CI Go job 无 Rust 工具链),强依赖会让 CI 红;skip 模式与 `web-check`/`plugin-build` 的「工具链缺席优雅跳过」既有惯例一致。

## 决策 7:构建链收敛为单一 `plugins/build.sh`

workspace 后 `cargo build` 的产物统一落 `plugins/target/...`;一个脚本构建 workspace 全部成员并把 `.wasm` 拷贝到各插件目录(文档引用的产物路径 `plugins/pii-masker/pii_masker.wasm` 等保持稳定,README/muniu.yaml 样例零改动)。Makefile `plugin-build` 与 CI workflow 都调它;顺手修正 Makefile 注释里陈旧的 `wasm32-wasip1` 措辞(实际目标自 HANDOFF 22 起就是 wasm32-unknown-unknown)。

## 决策 8:缺省规则词边界缺陷修复(2026-09-13 用户裁决,推翻「记为已知边界」的初审)

重写期发现:缺省规则(身份证/手机号)的 `\b` 锚点在 regex crate 下走 Unicode `\w` 语义,CJK 与数字同为词字符——「电话13912345678」这类**最高频中文形态**整体失明。初审拟记为已知边界,用户否决:紧贴中文正是中文场景的常态形态,这是缺陷必须修。

- **修复方案**:边界判定从正则锚点(`\b`)上收到引擎层谓词——模式裸匹配,命中后校验两侧是否紧邻 ASCII 字母数字:紧邻则拒(保住「不啃长串」的防截断性质),CJK/标点/空白视为边界(紧贴中文命中)。crate 无 lookaround,锚点路线无法两全;命中区间拼接替换不消费边界字符,ASCII 分隔的相邻号码场景零回归。
- **语义变化两处,均有测试钉死**:①缺省规则与无边界自定义规则在「更长 ASCII token 内部」的子串命中,由旧引擎可命中收敛为拒判(如 `ab1234cd` 中 `1234` 不再替换;确需子串语义未来经配置加显式开关,暂无消费者);②占位符替换从 `NoExpand` 改为引擎拼接,行为等价(永不 $ 展开)。
- 缺省规则模式相应去 `\b`(引擎已统一施加,保留 `\b` 会让紧贴中文继续失明);规则顺序(先长后短)不变。

## 风险与对策

- **宏展开的编译错误可读性**:`plugin!` 只做薄转发(生成 shim 调 `__abi`),类型不匹配错误仍落在用户函数签名上;SDK lib.rs 顶部放完整可拷贝示例。
- **重构引入 ABI 行为漂移**:三重护栏——Rust 单测(插件逻辑)、Go 真工件钉子(宿主视角契约)、真机冒烟(端到端注册/改写/热替换)。
- **workspace 迁移丢状态**:Cargo.lock 内容上移合并(依赖集合不变:regex/serde/serde_json 及传递依赖),`pii-masker/Cargo.lock` 删除;CI 与本地统一走 `plugins/build.sh`,无第二构建路径。
- **`preserve_order` feature 归属**:serde_json 的 `preserve_order`(pii-masker 现用,改写产物保键序)是全局 feature,由插件 crate 声明即对整图生效;两插件都显式声明,避免「谁开 feature」的隐式耦合。
