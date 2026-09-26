# ADR-0013:Anthropic Messages 入站——边缘翻译 v1

- 状态:已接受(2026-09-26;用户点名升格——ADR-0012 备选中「Messages 入站维持 backlog」由 owner 指令解除)

## 决策

muniu 新增 Anthropic Messages 入站形态(`POST /v1/messages`),复用 ADR-0012 确立的**边缘翻译**架构:入站请求在管道入口前一次性翻译为规范 chat 形态,既有管道(参数治理/语义缓存/插件/限流/failover/计量)零改动;响应与流式事件反向映射为 Anthropic message 对象与 `message_start/content_block_*/message_delta/message_stop` 事件序列。

1. **v1 范围**:文本 + 函数调用全链(tool_use/tool_result 与 chat tool_calls 互译,工具多轮请求侧完整),流式与非流式;`reasoning_content` 映射为无签名 thinking 块(展示形态)。
2. **鉴权头适配**:Anthropic 客户端以 `x-api-key` 携带 key——装配层 `AdaptAnthropicKey` 仅对该路径把缺失的 Authorization 补写为 Bearer,verifier 单点不动。
3. **明确不做(v1)**:多模态内容块(image/document 等,400 `content_block_unsupported`)、请求侧 thinking 块回传(丢弃:签名回传语义不成立)、`top_k`/`metadata`(静默丢弃)、服务端会话存储(与 ADR-0012 同款无状态边界)。
4. **已知偏差(登记于 proxy-and-streaming.md「Anthropic 入站翻译」)**:`message_start.usage.input_tokens` 恒 0,真实用量在 `message_delta.usage` 补全;管道内错误信封形态与 chat 同构(SDK 读 error.message 可读)。

## 理由

- **生态位**:Claude Code 等 Anthropic 协议客户端已成编码流量主力;国内主流厂商普遍设有官方 Anthropic 分支端点(docs/provider-presets.md 表 B)。支持 Messages 入站后,「Anthropic 客户端 ↔ muniu ↔ 任意出站家(OpenAI 兼容/Anthropic 分支)」闭环成立,入站格式矩阵(completions/responses/messages)补齐主流三家。
- **边缘翻译 vs 管道改造**:与 ADR-0012 同款论证——N+M 边缘适配器而非 N×M 管道感知;行为等价性用「与等价 chat 请求逐字节同构」钉子锁死。
- **x-api-key 适配放装配层而非改 verifier**:verifier 是契约级单点(chat/responses/admin 共用),为单一入站形态改动其签名影响面大;路径级中间件把适配限制在最小范围。

## 备选与否决

- Gemini generateContent 入站:客户端生态集中于 AI Studio 场景,当前零已知消费者——不立项,与 Gemini 出站轻形态同条件触发(出现真实需求 + 网络可达性解禁再立法)。
- 流式补 `event:` SSE 行与 message_start 预填 input_tokens:前者 SDK 不依赖;后者需管道预扫描无法实现,超集补全已在 message_delta——均否决。

## 后果

- `internal/protocol/anthropic_in.go` 增 Messages DTO 与双向翻译纯函数(测试先行域);`internal/proxy/messages.go` 增 handler;路由 `POST /v1/messages` 进漂移门禁。
- 出站候选为 anthropic 桥接(官方或各家分支端点)时,工具多轮边界不适用——Messages 入站的 tool 请求侧翻译完整;chat 入站经 anthropic 出站的往返仍受 ADR-0011 前登记边界约束(bridge_tool_roundtrip_unsupported)。
- 与 ADR-0012 的备选记录衔接:本 ADR 即该 backlog 的升格落地;spec 基线以本文 + 设计文档契约为准(openspec change 后补立项)。
