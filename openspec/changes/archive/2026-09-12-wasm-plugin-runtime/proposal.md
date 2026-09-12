# 插件机制:WASM 插件运行时(ADR-0011 立项)

## Why

ROADMAP backlog 长期登记「插件机制(WASM)」未落地,源码 grep 零命中;现有扩展点为 Go 接口 + 注册工厂(Adapter/Sink/PricingDirectory),无动态插件。2026-09 生态调研结论:

- 底座无可争议:wazero(v1.12.0,活跃维护,Wasm I/O 2026 专场报告)是 Go 生态唯一零依赖纯 Go wasm 运行时,与本项目 CGO_ENABLED=0 纪律天然契合;Traefik v3(wasm 中间件)、Mosn(proxy-wasm 扩展)、Dapr 均以 wazero 宿主生产运行。
- Extism go-sdk 底层同为 wazero 且纯 Go——「被迫自研宿主」的前提不成立;自研薄 ABI 的真实理由是 LLM 语义钩子形态自由与 ABI 掌控(含学习价值),属工程偏好而非被迫,design 如实记录裁决。
- **token 计数 wasm 化是伪需求**:BPE 为微秒级 CPU 操作,跨 wasm 边界只增开销;tiktoken-go 离线嵌入已是现成最优解,调研证实无任何公开收益数据。
- 形态对标:Higress 的 41 个 AI 插件(proxy-wasm / Envoy+Istio 重架构)在功能列表上不可胜出;muniu 的差异化位置是「自托管、单二进制、CGO_ENABLED=0、可沙箱执行不可信扩展逻辑」的轻量 LLM 网关——该位置无人占据;最接近的 Bifrost 已从 wasm 插件路线撤退重构,其教训(实例池/超时/内存限制缺失)正是本变更的工程含金量所在。

首个插件场景选定**请求载荷 PII 脱敏(正则规则,可热替换)**:这是「必须在沙箱里跑不可信代码」的正当场景——规则集可被非网关开发者扩展,出错只损失一个实例而非网关进程。与 token 计数(自家确定性逻辑,无沙箱正当性)相反。

## What Changes

1. 新增依赖 wazero(依赖白名单更新,stdlib-first 原则下首个运行时级依赖,选型由 ADR-0011 承载)。
2. **插件 ABI v0(自定薄 ABI)**:wasi_snapshot_preview1 + 自定义 module;明确不承诺 proxy-wasm 0.2.1 兼容(理由见 design);宿主-插件以线性内存 offset/len 交换载荷,二进制内置 ABI 版本字节。
3. **请求生命周期挂点**:入站 JSON body 检查/改写钩子,位于限流之后、上游候选循环之前(先于参数治理:插件面向业务载荷、治理面向模型参数,插件产物仍完整过治理);拒绝语义走既有错误信封;改写以响应头留痕。
4. **资源隔离与故障 containment**:每插件实例内存上限、单次调用超时(wazero epoch 中断)、panic 隔离、实例池复用;默认 fail_closed(插件异常即阻断并计指标),可按插件显式配置 fail_open。
5. **插件注册与生命周期**:本地文件 + 管理 API 注册(SHA-256 固定版本,注册即校验二进制与 ABI 版本),启停/热替换不打断在途请求;管理 API 契约(contracts/openapi.yaml)新增插件端点。
6. **可观测**:per-plugin 调用计数/耗时/拒绝/containment 计数,进既有 /metrics 零依赖文本协议。
7. **首发样例插件 pii-masker**(Rust,wasm32-unknown-unknown,与宿主同仓 plugins/ 目录独立 crate):正则规则脱敏请求 messages 载荷,含构建脚本与 CI 模板。

## Non-goals

- **proxy-wasm 0.2.1 ABI 兼容**:语义作参考,不承诺兼容——LLM 载荷钩子与 HTTP 代理语义错位,一旦承诺兼容即被一个 vNEXT 多年未定稿的 ABI 演进绑架。
- **响应流逐 chunk 钩子**:SSE 热路径(6μs/chunk 基线),首版不碰,backlog 保留。
- **token 计数插件**:伪需求(见 Why),tiktoken-go 原生路径不动。
- **Component Model/WIT**:wazero 尚未支持,raw ABI 是当前唯一纯 Go 路线。
- **控制台插件管理页**:管理 API 先行,web 控制台页面后续按需。
- **插件市场/远程分发/租户级插件**:属控制面产品范畴(另立仓库既定边界)。

## Capabilities

### New Capabilities

- plugin-runtime:插件注册与生命周期、请求体钩子语义、资源隔离与故障 containment、性能护栏与可观测。

### Modified Capabilities

(无既有能力的需求变更。)

## Impact

- **落点**:`internal/plugin/`(新包,依赖方向:仅依赖 protocol,被 proxy 消费,不反向依赖业务包)、`internal/server/` 链路挂点、`internal/admin/`(注册端点)、`internal/metrics/`、`contracts/openapi.yaml`(新增插件端点=契约变更,须同步 conformance)、`muniu.example.yaml`、Makefile(插件构建目标)、`docs/design/architecture.md`(链序契约增节)。
- **依赖白名单**:wazero 一项,ADR-0011 记录选型;白名单外零新增,`make tidy` 核对依赖树。
- **性能红线**:插件禁用时转发路径零新增开销(零分配短路);启用时 per-plugin 开销可观测,压测对照既有基线(38,498 rps / 首字节 p50 1.15ms / 6μs/chunk)记录增量,禁用态无回退。
- **文档联动**:ADR-0011、`docs/design/plugin-abi.md`(ABI v0 冻结文档)、architecture.md 增「插件运行时」节、HANDOFF 验收记录。
