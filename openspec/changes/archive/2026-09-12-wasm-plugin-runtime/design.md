# 插件运行时设计(WASM 插件,ADR-0011)

## Context

- backlog 登记「插件机制(WASM)」;现有扩展点为 Go 接口 + 注册工厂模式,无动态插件。
- middleware 链序即契约:request_id → recover → access_log → auth → quota → ratelimit → handler(ADR-0002,stdlib-first,`internal/server/middleware.go`)。
- 热路径现状:流式转发 6μs/chunk(`internal/proxy/stream.go`);请求治理在 Raw 字节层改写(`internal/proxy/governance.go`,attempt 循环内)。
- 包依赖方向(AGENTS.md):protocol 是 DTO 单源 ← adapter/proxy ← 编排层;新包 plugin 须遵守同向依赖,被 proxy 消费。
- 调研事实(2026-09):wazero v1.12.0 活跃(保守但持续,Component Model 未支持),零依赖纯 Go 唯一选;Extism go-sdk 底座同为 wazero 纯 Go,但 PDK call/host-function 协议面向通用函数插件,与请求生命周期钩子形态错位;proxy-wasm 现实标准 0.2.1,vNEXT 多年未定稿;Bifrost wasm 插件路线撤退(实例池/资源限制缺失);Traefik http-wasm 宿主有内存泄漏前车(issue #11119);Rust wasm32-unknown-unknown tier 2 稳定,需避开随机数依赖与 wit-bindgen(wazero 无 Component Model);wasm 词表/大数据不应嵌入二进制(实例化慢),走宿主注入。

## Goals / Non-Goals

**Goals**

- 插件崩溃/超时/超内存只损失该请求与该实例,网关进程与转发路径不受影响。
- 插件禁用时主路径零开销;启用时开销有界、可观测。
- 一个 Rust 样例插件走通「编写 → 构建 → 注册 → 生效 → 热替换 → 禁用」全流程。
- ABI v0 面最小且文档化,演进走 ADR。

**Non-Goals**

- proxy-wasm 兼容、响应流钩子、WIT、插件市场、租户级插件、控制台页面(见 proposal Non-goals)。

## Decisions

1. **宿主底座:wazero 直接使用,不引 Extism go-sdk。** *替代方案*:Extism go-sdk——拒绝:PDK 的 call/host-function 协议面向通用函数插件,与「检查/改写/拒绝」请求钩子形态错位;多一层抽象与依赖树;ABI 掌控权是本变更立项目的之一。*替代方案*:wasmtime-go/WasmEdge——拒绝:cgo,破坏 CGO_ENABLED=0 与 scratch 容器形态。
2. **自定薄 ABI v0,不承诺 proxy-wasm 兼容。** ABI 面:插件导出 `muniu_abi_version` / `plugin_start` / `hook_request_body`(offset/len → verdict + 经 `plugin_result()` 返回新 body offset/len)/ `malloc` / `free`;宿主 imports:`log(level, offset, len)` / `get_config(key_off, key_len, val_buf_off, val_buf_len)`。*实施期细化(2026-09-12,冻结于 docs/design/plugin-abi.md)*:版本探测以导出函数 `muniu_abi_version()->i32` 承载而非二进制定制 section——定制 section 的产出依赖链接器/后处理工具链(Rust 侧无标准属性),导出函数对任何工具链零门槛且可测试;结果通道补 `plugin_result()->i64`(verdict 与新 body 的 offset/len 需三个返回值,i32 单返回值装不下)。语义参考 proxy-wasm 0.2.1 与 Traefik http-wasm 教训(实例池化、显式释放),独立演进,ABI 变更走 ADR。
3. **挂点:ratelimit 之后、上游候选循环之前,单次执行。** *替代方案*:attempt 循环内逐候选执行——拒绝:failover 换道不应触发插件重跑(插件面向入站业务载荷,与上游候选无关);单次执行保性能预算。插件改写后的 body 仍完整经过参数治理,治理语义不受影响。
4. **失败语义默认 fail_closed。** *替代方案*:默认 fail_open——拒绝:首发场景是 PII 脱敏等安全类插件,插件失效时放行即泄露;与语义缓存「宁缺勿错」同哲学。fail_open 为 per-plugin 显式配置项,example 配置给注释警示。
5. **载荷交换:线性内存 offset/len,单次拷贝。** 宿主将 body 写入插件实例内存 → 调用钩子 → 插件在自身内存构造新 body → 宿主拷回。请求体大小沿用既有上限,超限直接 413,不进插件。
6. **资源限制默认值**:每实例内存 64MiB、单次调用超时 50ms(wazero epoch 中断)、实例池 8/插件(并发复用、空闲回收)。全部可配置;超时/超内存即计 containment 指标并按决策 4 处置。
7. **插件分发:本地文件 + 管理 API 注册,SHA-256 固定版本。** 注册即校验(wasm magic + ABI 版本字节探测),不匹配拒载并留审计;热替换 = 注册新版本 → 新请求生效,在途请求完成于旧实例。
8. **样例插件与宿主同仓,plugins/ 独立 crate。** *替代方案*:独立 git 仓——拒绝:ABI 联调期同仓降低闭环成本;目录独立成 crate,ABI 稳定后可平移成仓。

## Risks / Trade-offs

- [ABI v0 设计失当导致 v1 破坏性演进] → ABI 面刻意最小(6 导出 + 2 imports);版本字节内置二进制头部;v0 期只服务自家样例插件,不对第三方承诺。
- [wasm 实例池内存驻留] → 池上限 + 空闲回收;内存上限硬顶;指标暴露实例数;压测含插件启用形态。
- [wazero AOT 编译冷启动(数百 ms 级)] → compilation cache 常驻进程,注册时编译一次,不进请求路径;启动延迟可观测。
- [fail_closed 误伤(插件规则错误导致全量阻断)] → 插件级启停开关;containment 指标 + 审计;样例插件规则热替换无需重启。
- [Rust 插件工具链对贡献者的门槛] → plugins/ 提供构建脚本与 CI 模板;宿主侧不绑 Rust 工具链(任何符合 ABI 的 wasm 均可加载)。

## Migration Plan

1. ADR-0011 合入 → 依赖白名单更新(wazero)。
2. ABI v0 文档冻结(`docs/design/plugin-abi.md`)→ 宿主实现 + wat 最小插件测试先红后绿。
3. 链路挂点 + 管理 API + 指标 → conformance 契约测试补插件形态用例。
4. pii-masker 样例插件(Rust)→ 压测对照(禁用/启用两形态,记录增量)→ `make check` 全绿 + HANDOFF 验收记录。

## Open Questions

- 管理 API 端点形态(注册/列表/启停)与既有 admin 鉴权、分页约定的对齐细节,实施时按 `internal/admin/` 现状定。
- 插件配置(get_config)的 schema 校验深度:首版字符串直通,结构化校验待真实插件需求出现。
