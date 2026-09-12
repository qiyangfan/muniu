# 任务清单:插件机制·WASM 插件运行时(ADR-0011)

> 纪律:新域测试先行(先红后绿);`make check` 全绿方可勾选,不留半勾;依赖白名单外禁止 `go get`;契约级文件(contracts/openapi.yaml)变更须同步 conformance。

## 1. 前置立法与依赖

- [x] 1.1 ADR-0011:WASM 插件运行时选型(wazero 直用 / 自定薄 ABI v0 / 不兼容 proxy-wasm 的理由与代价 / Extism 为何不引);验证:ADR 合入,ROADMAP backlog「插件机制(WASM)」条目挂接。
- [x] 1.2 依赖白名单更新(wazero)+ go.mod 接入;验证:`make tidy`,依赖树仅新增 wazero 及其传递依赖,清单记入任务备注。
  - 备注:go.sum 仅新增 `github.com/tetratelabs/wazero v1.12.0` 一行(main)及其自身 go.mod 行——**零传递依赖**,直接依赖 5→6。

## 2. ABI v0 与测试先行

- [x] 2.1 ABI v0 文档冻结(导出/imports/内存协议/版本字节/错误码),`docs/design/plugin-abi.md`;验证:文档定稿,与 design Decisions 逐条对得上。
- [x] 2.2 wat 手写最小插件 fixture + 宿主加载测试(加载/版本探测/坏插件拒载);验证:先红后绿,`-race` 全绿。
  - 备注:本机与 CI 均无 wabt 工具链,fixture 以纯 Go 字节装配器实现(`internal/testkit/wasmplugin.go`,等价 wat 注释存档于分支),12 种行为分支覆盖 ABI 全貌;工具链无关且 CI 稳定。

## 3. 宿主运行时

- [x] 3.1 实例池与生命周期:compilation cache、池化复用、空闲回收、实例内存上限;验证:并发单测,`-race` 无数据竞争。
- [x] 3.2 资源隔离:调用超时(epoch 中断)、超内存终止、panic 隔离、containment 计数;验证:故障注入测试(超时/超内存/插件 panic 三态,先红后绿)。

## 4. 链路挂点

- [x] 4.1 请求体钩子挂点(ratelimit 之后、候选循环之前,单次执行),拒绝走既有错误信封,改写留痕响应头;验证:集成测试(拒绝/改写/放行三态)+ 禁用态零开销基准探针。
- [x] 4.2 fail_closed / fail_open per-plugin 语义与配置解析(muniu.yaml plugins 节);验证:配置测试两态行为,`muniu.example.yaml` 注释样例同步。

## 5. 管理 API 与可观测

- [x] 5.1 插件注册/列表/启停/版本替换端点(SHA-256 固定,注册即校验);验证:`contracts/openapi.yaml` 增补 + conformance 用例 + 管理面集成测试。
- [x] 5.2 per-plugin 指标(调用/耗时/拒绝/containment)进 /metrics;验证:metrics 文本协议测试。

## 6. 样例插件与压测

- [x] 6.1 plugins/pii-masker(Rust,wasm32-unknown-unknown):正则脱敏钩子,构建脚本 + CI 模板;验证:全流程手工冒烟(构建→注册→生效→热替换→拒绝→禁用),证据贴 HANDOFF。
  - 备注(目标语言取舍):原定 wasm32-wasip1,实证 wasip1 self-contained libc 的 C dlmalloc 与 ABI malloc/free 导出同名冲突(lld 多重定义,任一裁决都破坏一方),且插件无需任何 WASI 能力——改 **wasm32-unknown-unknown**:零 WASI 导入沙箱面更小,冲突消除;proposal/design/tasks/ABI 文档措辞已同步。CI 模板:`.github/workflows/plugins.yml`。
- [x] 6.2 压测对照:插件禁用/启用两形态,对照既有基线记录增量与 containment 表现;验证:docs/benchmarks/ 新增记录,禁用态无回退。

## 7. 文档与收尾

- [x] 7.1 `docs/design/architecture.md` 增「插件运行时」节(链序位置与请求生命周期契约修订);验证:文档与实现一致性走查。
- [x] 7.2 `make check` 全绿 + conformance 全量 + HANDOFF 验收记录;验证:全绿输出与验收证据贴 HANDOFF。
  - 备注:make check 全绿(19 包 race + lint 0 issues + web typecheck/lint,exit=0);路由漂移门禁绿(openapi.yaml ≡ 装配路由);验收证据见 HANDOFF 验收记录 22。
