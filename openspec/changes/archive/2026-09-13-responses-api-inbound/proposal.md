# Responses API 入站(无状态翻译 v1)

## Why

2026 年 OpenAI 生态新客户端(Codex CLI、Agents SDK 等)以 Responses API 为默认形态,网关不支持即把该生态整体挡在门外——这是四项升格候选中唯一的战略级缺口。ADR-0012 已立法:**边缘翻译架构 + 无状态 v1**(翻译为规范 chat 请求后复用既有管道,治理/缓存/插件/限流/failover/计量零改动)。

## What Changes

- `POST /v1/responses`:请求翻译(`instructions`/`input` items/function tools/`tool_choice`/`max_output_tokens`/`temperature`/`top_p`/`stream` → 规范 chat 请求),进入与 chat completions 同一的管道入口,鉴权/配额/限流链同规。
- 响应映射:chat completion → Responses 对象(`object`/`status`/`output[]`/`usage`);流式 SSE → 最小事件集(`response.created` / `response.output_text.delta` / `response.function_call_arguments.delta` / `response.output_item.added`+`done` / `response.completed`,completed 携带 usage),顺序不变式:created 首帧、completed 终帧。
- 无状态边界:`GET /v1/responses/{id}` 恒 404 `not_found`(文案如实说明);请求携带 `previous_response_id` → 400 明确拒绝(静默忽略会丢失客户端上下文,属危险失败);v1 不支持的多模态输入项(input_image 等)→ 400 明确拒绝。
- reasoning 只读透出:上游 `reasoning_content` → reasoning 输出项,缺失即省略。
- 跨协议缓存共享:语义缓存键 = 翻译后规范请求——同一别名的等价 chat 请求与 Responses 请求天然共享缓存条目。
- conformance fixture 与表驱动翻译测试先行(协议适配 = 测试先行域)。

## Capabilities

### New Capabilities

- `responses-inbound`: Responses 入站翻译契约、流式事件映射、无状态边界与管道等价性。

### Modified Capabilities

(无)

## Impact

- 代码:`internal/protocol`(Responses DTO + 双向翻译纯函数)、`internal/proxy` 或 `internal/server`(handler + 路由)、`cmd/muniu`(装配)、`contracts/openapi.yaml` + api-types。
- 文档:`docs/design/proxy-and-streaming.md` 增「Responses 入站翻译」节(映射表冻结);README 功能表。
- 前置依赖:`upstream-egress-batch` 先行——工具多轮经 anthropic 出站的错误语义需有明确契约可援引。
- 风险面:纯新增端点,既有 `/v1` 零改动;等价性以「同一别名等价 chat 请求」钉子锁死。
