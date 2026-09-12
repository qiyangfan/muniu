# ADR-0012:Responses API 入站——边缘翻译、无状态 v1

- 状态:已接受(2026-09-12)

## 决策

muniu 新增 OpenAI Responses API 入站形态(`POST /v1/responses`),采用**边缘翻译**架构:v1 为无状态翻译层——入站请求在管道入口前一次性翻译为规范内部形态(OpenAI chat completions),复用既有管道(参数治理/语义缓存/插件/限流/failover/计量零改动);响应与流式事件做反向映射。

1. **规范内部形态唯一**:「N 入站形态 × M 出站适配」的复杂度收敛为 N+M 个边缘适配器;Responses 入站与 Anthropic 桥接(出站)对称,共享同一 canonical。
2. **v1 范围**:文本 + 函数调用(function tools/tool_choice),流式与非流式;上游 `reasoning_content` 只读映射为 reasoning 输出项,缺失即省略;usage 计量完全沿用上游 usage(零新计量口径,别名牌价不变)。
3. **无状态边界**:服务端不存储 response 对象、不支持 `previous_response_id`(上下文管理归客户端);`GET /v1/responses/{id}` 恒 404(`not_found`,文案如实说明无状态网关不存储)。
4. **明确不做(v1)**:background 模式、内置/hosted 工具(web_search/code_interpreter/computer_use)透传、多模态输入项(input_image 等)、Azure 专属字段。

## 理由

- **2026 生态位**:Codex CLI、Agents SDK 等 Responses 原生客户端已成新流量默认;不支持即把该生态挡在门外,「OpenAI 兼容入站」的承诺名存实亡。
- **边缘翻译 vs 管道改造**:让管道感知第二入站形态,治理/缓存/插件每个子系统都要双形态测试,组合爆炸;边缘翻译后全部子系统零改动,行为等价性用「同一别名的等价 chat 请求」钉子测试锁死。
- **无状态是网关惯例**:LiteLLM/one-api 均不做服务端会话存储;有状态化引入存储层、GC 与多副本一致性问题,与 ADR-0005 无状态数据面形态直接冲突。

## 备选与否决

- 服务端存储 + `previous_response_id`(v1):违背无状态数据面形态,代价与个人场景收益不成比例——否决,记 backlog(出现真实消费者再立法)。
- 仅对原生支持 Responses 的上游透传:把兼容性责任退回客户端配置,违背「一份配置跨家换道」立身之本——否决。
- 先做 Anthropic Messages 入站:消费生态窄(Anthropic 系客户端);Responses 覆盖面更大——Messages 入站维持 backlog。

## 后果

- `internal/protocol` 增 Responses DTO 与双向翻译纯函数(测试先行域:协议适配);翻译失败(非法 items/不支持项)→ 400 `bad_request` 明确拒绝。
- 路由仅注册 `POST /v1/responses`;`GET /v1/responses/{id}` 显式注册返回 `not_found` 信封(比 mux 裸 404 诚实)。
- spec 基线走 openspec change `responses-api-inbound`;proxy-and-streaming.md 增「Responses 入站翻译」节。
- 与桥接交互:Responses 入站的工具多轮经 anthropic 出站时,受「桥接工具多轮显式边界」约束(change `upstream-egress-batch` 先行落地)——明确报错,不静默。
