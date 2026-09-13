# Tasks — plugin-rust-sdk

> 允许新增依赖:**零**(Cargo 侧仅既有 regex/serde/serde_json;Go 侧零)。任务粒度即勾选粒度:一个任务一次收尾(实现+测试+相关门禁),不留半勾。

## 1. Workspace 化与 SDK crate

- [x] 1.1 `plugins/Cargo.toml` workspace 根(members: sdk/pii-masker/json-redactor)+ `plugins/.gitignore`(target/ 与产物 wasm)+ `Cargo.lock` 上移、删除 `pii-masker/Cargo.lock`;`cargo metadata` 校验 workspace 解析成功
- [x] 1.2 `plugins/sdk`(muniu-plugin-sdk)crate 骨架:`src/__abi.rs` 全量迁移现行内存策略——宿主导入(`get_config`/`log`)、双 arena 代际回收(A/B 区 25MiB)、scratch 错位(0-256/256+/+1280)、`ensure` 续页循环、malloc/free/plugin_result 语义,`cfg(target_arch)` 双态(宿主 stub);模块头固化三条不变量注释(design 决策 2)
- [x] 1.3 `src/lib.rs` 安全 API:`Config`(`raw`/`json::<T>` 两段式封装)、`log(Level, &str)`、`Verdict` 三态、`plugin!` 宏(wasm32 cfg 下生成 6 导出 shim;crate 根别名转发解决宏卫生;错误码 0/1/2/3 映射保持);lib 顶部完整插件示例文档
- [x] 1.4 SDK 宿主单测:配置解析纯函数(合法/非法 JSON/键缺失)、Level 映射;`cargo test -p muniu-plugin-sdk` 4 用例绿

## 2. pii-masker 基于 SDK 重写(行为不变 + 词边界缺陷修复)

- [x] 2.1 `pii-masker/src/lib.rs` 重写:零 unsafe、~90 行业务代码(init 读规则+缺省规则 / walk_value / mask_str);`plugin_start` 错误码与日志行为与重构前一致
- [x] 2.2 pii-masker 宿主单测(首个 Rust 侧测试):规则解析(合法/非法正则/非法 JSON)、遍历覆盖(messages[].content 字符串、content parts text 段、embeddings input 字符串与数组)、替换序(长规则先跑)、字面占位符;**含 2.1a 词边界缺陷修复**(用户裁决,design 决策 8):缺省规则紧贴中文命中 + ASCII 长串内拒判,`cargo test -p pii-masker` 9 用例绿
- [x] 2.3 wasm 构建回归:重建 `pii_masker.wasm` 落位,行为经 Go 真工件钉子(4.1)与真机冒烟(6.1)验证

## 3. json-redactor 插件

- [x] 3.1 `plugins/json-redactor` crate:JSON Pointer(RFC 6901 `~0`/`~1` 转义)+ `*` 通配段解析、remove/replace 操作、不可解路径静默跳过、modified 精确记账;配置校验(指针形态/replace 必带 value)失败拒载
- [x] 3.2 json-redactor 宿主单测:指针转义、数组/对象通配、嵌套路径、remove 与 replace、miss 跳过、类型不匹配跳过、校验负面用例;`cargo test -p json-redactor` 11 用例绿;wasm 产物 `json_redactor.wasm` 构建验证

## 4. Go 回归钉子与构建链

- [x] 4.1 `internal/plugin` 真工件集成测试(工件缺席 t.Skip):经真实运行时加载重构后 `pii_masker.wasm`——版本探测=0、plugin_start=0、钩子改写 verdict=1+结果通道+宿主拷出断言、无命中 verdict=0、坏规则配置 start≠0;另含 json-redactor 真工件用例与「紧贴中文脱敏」端到端断言;`go test -race ./internal/plugin/ -run TestRealArtifact` 3 用例绿(踩坑留证:探测超时覆盖 wazero 冷编译,测试放宽 ProbeTimeoutMs=60s 硬顶)
- [x] 4.2 `plugins/build.sh` 单一构建脚本(workspace 全量构建+产物落位+rustup env 兜底);Makefile `plugin-build` 接脚本并修正 wasip1 陈旧措辞;`.github/workflows/plugins.yml` 改 workspace 构建双插件 artifact + Rust 宿主单测步;本地实跑 `./plugins/build.sh` 与 `make plugin-build` 双路径验证

## 5. 文档

- [x] 5.1 新增 `plugins/README.md`(工作区总览、SDK 快速上手含完整最小插件示例、构建与产物路径表);pii-masker/json-redactor README 更新(构建节指向 workspace 脚本、json-redactor 新写含配置样例与通配风险说明);`docs/design/plugin-abi.md` 参考实现行补 SDK 指针(契约文字零改动)
- [x] 5.2 `muniu.example.yaml` plugins 段补 json-redactor 注释样例;`make check` 全绿收口(vet + lint 0 issues + web 门禁 + 全包 race + coverage 68.4% PASS)

## 6. 端到端验收与收尾

- [x] 6.1 真机冒烟(临时实例 127.0.0.1:18784,dashscope qwen-plus 真上游,模式同验收记录 22 ⑨):注册(启动日志「started」宿主透传)/生效(紧贴中文手机号 + metadata 删除,`X-Muniu-Plugin: pii-masker,json-redactor`)/热替换(自定义规则 replaced=true,复述测试上游收到的即 `[REDACTED]`——载荷脱敏最强实证)/拒载(非法正则 400 plugin_rejected「退出码 1」,旧版继续服务)/禁用(原文透传无留痕)/metrics(calls{verdict} 四计数 + duration 直方图 le="5" 全中)
- [x] 6.2 Rust 侧 lint 门禁:`cargo clippy --workspace --all-targets -- -D warnings` 0 warning + `cargo fmt` 基线建立 + `cargo fmt --check` 通过;证据贴 HANDOFF 验收记录;openspec validate + archive;HANDOFF 四块更新
