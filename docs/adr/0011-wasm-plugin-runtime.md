# ADR-0011:WASM 插件运行时——wazero 直用、自定薄 ABI v0、不承诺 proxy-wasm 兼容

- 状态:已接受(2026-09-12)

## 决策

以 **wazero(v1.12.0)直用**为底座引入 WASM 插件运行时,新增 `internal/plugin` 包(依赖方向:仅依赖 protocol,被 proxy 消费);插件 ABI 为**自定薄 ABI v0**(6 导出 + 2 imports,见 `docs/design/plugin-abi.md`),首个场景为请求载荷 PII 脱敏插件(`plugins/pii-masker`,Rust wasm32-unknown-unknown)。

1. **宿主底座:wazero 直接使用,不引 Extism go-sdk。** Extism 的 PDK call/host-function 协议面向通用函数插件,与「检查/改写/拒绝」的请求生命周期钩子形态错位;多一层抽象与依赖树,且 ABI 掌控权本就是本次立项的目的之一。wasmtime-go/WasmEdge 均 cgo,破坏 `CGO_ENABLED=0` 与 scratch 容器形态——wazero 是 Go 生态唯一零依赖纯 Go wasm 运行时,与本项目单二进制纪律天然契合(Traefik v3、Mosn、Dapr 均以 wazero 宿主生产运行)。
2. **自定薄 ABI v0,不承诺 proxy-wasm 0.2.1 兼容。** proxy-wasm 语义(HTTP 代理钩子)与 LLM 载荷钩子错位;一旦承诺兼容即被一个 vNEXT 多年未定稿的 ABI 演进绑架。语义仅作参考,吸取 Traefik http-wasm 的教训(实例池化、显式释放、内存上限),独立演进,ABI 变更走 ADR。
3. **挂点:限流之后、上游候选循环之前,单次执行。** 插件面向入站业务载荷,与上游候选无关——failover 换道不得触发插件重跑;单次执行保性能预算。插件产物仍完整经过既有请求参数治理(ADR-0010),两层正交。
4. **失败语义默认 fail_closed。** 首发场景是 PII 脱敏等安全类插件,插件失效时放行即泄露;与语义缓存「宁缺勿错」同哲学。fail_open 为 per-plugin 显式配置项。
5. **token 计数不 wasm 化。** BPE 是微秒级 CPU 操作,跨 wasm 边界只增开销;tiktoken-go 离线嵌入已是现成最优解,无公开收益数据——伪需求,不做。

## 理由

- **沙箱正当性**:扩展点此前全部是 Go 接口 + 注册工厂(Adapter/Sink/PricingDirectory),编译期固定;「规则集可被非网关开发者扩展、出错只损失一个实例而非网关进程」的需求,只有进程内沙箱能满足。PII 脱敏正则是正当场景——规则可能来自安全团队而非网关开发者,代码不可信。
- **差异化定位**:Higress 的 41 个 AI 插件(proxy-wasm/Envoy+Istio 重架构)在功能列表上不可胜出;muniu 的位置是「自托管、单二进制、CGO_ENABLED=0、可沙箱执行不可信扩展逻辑」的轻量 LLM 网关。最接近的 Bifrost 已从 wasm 插件路线撤退重构,其教训(实例池/超时/内存限制缺失)恰是本变更的工程含金量。
- **依赖纪律**:stdlib-first 下首个运行时级依赖,白名单单列 wazero(零传递依赖),`make tidy` 后依赖树可核对。

## 备选与否决

- Extism go-sdk:见决策 1,否决。
- wasmtime-go / WasmEdge:cgo,破坏 CGO_ENABLED=0,否决。
- proxy-wasm 0.2.1 兼容:LLM 载荷钩子与 HTTP 代理语义错位,vNEXT 未定稿,否决(语义参考仅限教训吸取)。
- token 计数插件:伪需求,见决策 5。
- Component Model/WIT:wazero 尚未支持,raw ABI 是当前唯一纯 Go 路线;WIT 支持落地后再评估 ABI v1。

## 后果

- 依赖白名单新增 wazero 一项;`internal/plugin` 新包遵守包依赖方向(protocol ← plugin ← proxy)。
- `docs/design/plugin-abi.md` 冻结 ABI v0 契约;architecture.md 增「插件运行时」节修订链序契约。
- contracts/openapi.yaml 新增插件管理端点(契约变更,同步 conformance)。
- 测试先行域:宿主加载/版本探测/坏插件拒载、资源隔离三态(超时/超内存/panic)、链路三态(拒绝/改写/放行)、fail 两态、指标文本协议。
- 性能红线:插件禁用时转发路径零新增开销(零分配短路);启用形态压测对照既有基线记录增量。
