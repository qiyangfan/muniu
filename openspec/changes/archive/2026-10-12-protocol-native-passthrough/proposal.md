# Proposal

## Why

边缘翻译架构(ADR-0012/0013)让三种入站协议复用一条管道,但对「入站协议 = 上游原生协议」的流量制造了不必要的双重换译:Anthropic 客户端(如 Claude Code)→ muniu → Anthropic 分支端点,请求被 anthropic→chat→anthropic 往返翻译,响应再反向映射回来。翻译路径丢弃或显式拒绝的能力(image 多模态、thinking 回传、cache_control、top_k、metadata、stop_sequences、tool_choice=none)在这类同构流量上无谓失效,流式往返还引入 `message_start.usage.input_tokens` 恒 0 等登记偏差,工具多轮流量更被 bridgegate 整类豁免。类比外币结算:全球币种一律先转美元再结算,而同币种消费本应原币直结——owner 已点名要后者。

## What Changes

- **新增同构直通路径**:入站协议与候选适配器原生协议一致时(v1 范围:messages 入站 × `adapter: anthropic` 候选),出站 wire 使用客户端原始字节,仅改写 model 字段 + 注入鉴权头/URL;响应、流式事件、错误信封原样透传——零翻译、零字段丢失,上游真实 anthropic 事件(含真实 message_start usage)直达客户端。
- **Responses 原生直通(owner「能直连就直连」升格)**:Responses 入站 × 显式声明 `native_responses` 能力的候选直通(provider 配置声明,零自动探测)——官方 responses 对象/SSE 原样透传,仅改写 model 与鉴权;input_image 等部件经直通恢复原生支持。采声明制的实证依据:百炼 responses 端点多轮工具回放四路径全 400(2026-10-11 服务端缺口实证),「OpenAI 兼容」≠「responses 端点可用」,未声明的候选维持既有 chat 翻译兜底,端点修复后开启声明即切,零代码改动。
- **有状态粘性路由(owner 学习项目框架裁决收编)**:`previous_response_id` 在原生 lane 放行——provider 级亲和表(id→provider,TTL+LRU,集群复用共享后端)约束粘性路由(failover 域收缩至亲和 provider 内,跨 provider 换道对状态请求不发生,这是服务端状态定向单上游的显式语义);`GET /v1/responses/{id}` 改为亲和已知代理转发、未知自解释 404;状态请求旁路语义缓存;亲和未知/重启丢失走自解释降级;DELETE 代理登记 backlog。
- **路径选择为每 attempt 级**:换道跨越协议边界时(如 anthropic 主候选失败回落 deepseek),响应 writer 模式在首字节提交前切换(直通=恒等写,翻译=现有反向映射);已提交后绝不二次写出(沿用 trackWriter 不变式)。
- **控制面不变**:canonical chat 仍在边缘计算一次,参数治理判定、语义缓存指纹、TPM 估算、计量口径、插件请求体钩子全部照旧;spec「管道等价性」语义保持。
- **bridgegate 收窄**:工具多轮豁免仅作用于翻译路径(messages 入站 × anthropic 候选走直通,不受 `bridge_tool_roundtrip_unsupported` 约束);chat 入站 × anthropic 候选仍受既有边界。
- **边缘翻译失败降级为失格标记(惰性拒绝)**:含不可翻译内容块(image 等)的请求不再一律边缘即时 400——别名存在直通候选时先按直通服务(多模态原生支持);仅当翻译路径实际被需要(别名无直通候选,或直通候选耗尽回落翻译)时升格为既有 400 `content_block_unsupported`;别名无直通候选时行为与现状逐字节一致。翻译失格的请求跳过语义缓存检索与入库。
- **回落纪律**:两种情形该 attempt 放弃直通改走翻译路径——①能力目录对目标候选声明约束且治理实际干预(治理必须触达 wire);②插件改写了 canonical 请求体(直通原始体已过期)。
- **语义缓存双存**:直通响应同时存 canonical chat 形态(跨协议命中,既有等价性不变)与原生响应体+协议标签(同构命中零翻译恒等回放);缓存未启用/翻译失败时零翻译开销,不阻塞主链路。
- **明确不做(v1)**:responses 入站 × anthropic 候选的直译(保持现有组合翻译)、有状态 Responses 的 DELETE 代理与亲和跨重启迁移(亲和进程内 TTL,重启丢失走自解释降级并登记)、chat 入站 × anthropic 候选(仍走桥接翻译,与本变更无关)、Gemini 入站/出站(既有边界不动)、插件 ABI 协议中立化(维持 canonical + 回落,理由见 design 决策 9)。

## Capabilities

### New Capabilities

- `protocol-native-passthrough`:同构协议直通路由——每 attempt 的直通/翻译路径判定、wire 保真不变式(仅 model 改写)、控制面等价性(治理/缓存/计量/插件)、回落纪律、与 failover 换道及 bridgegate 的组合语义。

### Modified Capabilities

- `anthropic-bridge`:工具多轮边界的适用范围收窄——豁免判定从「候选是桥接型」收窄为「该 attempt 实际走翻译路径」;messages 入站经直通服务 anthropic 候选时不再触发 `bridge_tool_roundtrip_unsupported`。
- `responses-inbound`:「无状态边界」改写——翻译路径维持无状态(previous_response_id 拒绝、未知 id 404);原生 lane 新增有状态语义(亲和表、粘性路由、GET 代理转发)。

## Impact

- `internal/protocol`:Messages/Responses 入站 DTO 保留原始请求体与协议标签(现边缘翻译后原始体被丢弃)。
- `internal/adapter`:新增可选能力接口(直通 Prepare:原始体 + model/鉴权改写;流事件 meta-only 读侧),由 bridge 包实现;不改既有五方法契约。
- `internal/proxy`:messages/responses handler 保留原始体;`handleChat`/`attempt` 增每 attempt 路径判定;响应 writer 支持模式切换;bridgegate 条件收窄。
- `internal/config`:`providers[].native_responses` 布尔声明(缺省 false)进 schema 校验——本变更唯一配置面扩容。
- 文档:`docs/adr/0014-*`(同构直通路由决策)、`docs/design/proxy-and-streaming.md`(同构直通契约节)、`docs/design/upstream-adapters.md`(接口增补)、provider-presets.md(逐家标注 responses 原生端点可用性实证)。
- 无新依赖;语义缓存键、计量落账、指标路径标签机制不变。
