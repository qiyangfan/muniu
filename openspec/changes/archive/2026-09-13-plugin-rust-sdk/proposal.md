# 插件机制:Rust 插件 SDK(muniu-plugin-sdk)与样例插件工作区化

## Why

样例插件 pii-masker 把 ABI v0 的全部硬骨头——裸指针写入、双 arena 代际回收、`get_config` 两段式、scratch buffer 错位避让——写死在单一 `lib.rs`(~330 行,含大量 unsafe)。后果有二:

1. **可维护性债务**:`plugins/` 下每新增一个插件,要么整体复制这 ~200 行 unsafe 与内存策略代码,要么重新踩一遍已实证过的坑(arena 与 dlmalloc 同顶续页重叠 OOB、scratch 自读自写)。SDK 机制与插件语义耦合在同一文件,没有「写一个新插件」的合法路径。
2. **测试空洞**:Rust 侧零单测,插件逻辑(规则解析/JSON 遍历/替换语义)的正确性只靠 Go 侧真机冒烟兜底,违反仓库「测试先行」纪律的精神。

抽 SDK 是工程价值真实的重构:unsafe 收口到一处并文档化不变量,插件作者零 unsafe 只写钩子逻辑;插件纯逻辑首次可宿主单测;配套一个机制正交的第二插件证明抽象普适(只做一个插件撑不起「SDK 抽象住了」的结论)。

## What Changes

1. **plugins/ 改造为 Cargo workspace**:成员 = `sdk`(muniu-plugin-sdk)+ `pii-masker` + `json-redactor`;`Cargo.lock`/`target/` 上移 workspace 根。
2. **新增 muniu-plugin-sdk crate**:`plugin!` 声明宏生成 ABI v0 全部 6 个导出;`Config`(两段式 `get_config` 封装 + JSON 解析)、`log` 安全 API;`Verdict`(Pass/Rewrite/Reject)覆盖 ABI 钩子三态;**全部 unsafe 收口在单一内部模块**,arena 不变量(dlmalloc 预留/代际回收/scratch 错位)以文档形式固化。
3. **pii-masker 基于 SDK 重写**:~330 行 → ~80 行业务代码,零 unsafe;ABI 行为字节级不变(plugin_start 错误码、verdict 语义、缺省规则、日志);插件逻辑首次获得宿主单测。
4. **新增第二样例插件 json-redactor**:JSON Pointer(RFC 6901,`*` 通配段扩展)字段级 remove/replace 脱敏——与 pii-masker 的「内容扫描」正交的「结构定向」机制,证明 SDK 钩子抽象不绑定单一玩法;坏配置拒载语义与 pii-masker 一致。
5. **构建链**:workspace 化 `plugins/build.sh`(一键构建全部插件产物并落位到各插件目录);Makefile `plugin-build` 接新脚本并修正陈旧的 wasip1 措辞;CI `plugins.yml` 构建双插件产物。
6. **Go 侧零改动 + 一个回归钉子**:宿主实现、ABI 契约、openapi、依赖白名单均不动;新增「真 wasm 工件回归」集成测试(工件缺席自动 skip)——本地 `make check` 即覆盖真插件的注册/生效/改写链路,CI(Go 侧无 Rust 工具链)不受影响。
7. **文档**:新增 `plugins/README.md`(工作区总览 + SDK 快速上手);两插件 README 更新构建说明;`docs/design/plugin-abi.md` 参考实现节补 SDK 指针(契约文字零改动)。

## Capabilities

### New Capabilities

(无——本变更为插件侧工具链与样例资产重构,不改变网关任何 spec 级行为;按约定 `skip_specs: true`)

### Modified Capabilities

(无——`plugin-runtime` 主 spec 全部宿主侧行为不变;ABI v0 契约冻结不动;openapi/api-types 零变化)

## Impact

- **Rust**:`plugins/` 全量重组——sdk 新增(~400 行,全部 unsafe ≤1 模块)、pii-masker 重写、json-redactor 新增、两侧单测新增;Cargo 依赖零新增(SDK 与两插件仅用既有 regex/serde/serde_json)。
- **Go**:仅新增 1 个 skip-if-absent 集成测试(`internal/plugin`),宿主实现零改动。
- **构建/CI**:`.github/workflows/plugins.yml`、`Makefile`(plugin-build)、`plugins/build.sh`。
- **明确不动**:ABI v0 冻结契约、wazero 宿主、openapi.yaml、web 控制台、Go/Cargo 依赖白名单。
