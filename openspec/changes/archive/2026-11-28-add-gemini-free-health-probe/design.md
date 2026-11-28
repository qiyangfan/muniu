# Design: add-gemini-free-health-probe

## Context

见 proposal.md「Why」。现有探针适配器表把端点与鉴权形态收在 `internal/health/probe.go`；gemini 原生适配器出站使用 `x-goog-api-key`，基址样例为 v1beta。免费探针只消费 HTTP 状态码，不需要解析响应体。

## Goals / Non-Goals

**Goals:**

- Gemini 原生渠道默认获得零货币成本的免费控制面探针。
- 探针鉴权形态可扩展，不再以布尔字段叠加特例。
- `probe_model` 在 Gemini 上显式拒绝，不产生静默无效声明。

**Non-Goals:**

- 不实现 Gemini 推理探针（请求体形态不同，且用户只要求免费控制面探针）。
- 不改变探针取样策略（首键/禁用键问题由独立 change `health-probe-integrity` 处理）。
- 不改变数据面转发、key 池调度与 failover 判定表。

## Decisions

1. **探针端点仍由 `internal/health` 单点声明**。在 `adaptersWithProbe` 增加 `gemini: {freePath: "/models"}`，不把端点知识散到 config 或 adapter。免费探针与现有 OpenAI/Anthropic 共用 `probeSend`，只读取状态码。
2. **鉴权形态从 `anthropic bool` 收敛为枚举**。新增 `probeAuthBearer / probeAuthAnthropic / probeAuthGemini`；`setProbeAuth` 按枚举分支。理由：继续加第四个布尔字段会让「是否有 beta 头」「是否 x-api-key」组合失控；枚举把差异收在一个 switch 内，测试可逐格断言。备选是继续加 `gemini bool`，否决。
3. **免费与推理探针能力分离**。`ProbeSupported` 继续表示免费探针能力；新增 `InferenceProbeSupported` 表示 `inferencePath` 非空。`manager.tick` 与装配层 `healthChannelBase` 的 `probe_model` 设置都改用后者，防止 Gemini 误走 OpenAI 形态的推理探针。
4. **配置校验拒绝 Gemini 的 `probe_model`**。`config.validateHealth` 调用 `health.InferenceProbeSupported`，不支持的适配器直接报错；不在运行期静默忽略。理由：配置写了但永远不生效属于伪装成功，与本仓 `native_responses` 校验先例一致。备选是运行时 warn 后忽略，否决。
5. **不新增探针解析逻辑**。Gemini `models.list` 的响应体不参与健康判定，只取 2xx/429/其他；429 仍判「存活但受限」不计成败。

## Risks / Trade-offs

- [自定义 Gemini 代理不透传 `/models`] → 探针会判失败并熔断渠道；渠道级 `free_probe: false` 可退订，文档注明端点要求。
- [探针仍取首把 key] → 已知缺陷，独立 change `health-probe-integrity` 根修；本变更不改变取样面。
- [默认开启新增外呼] → 每渠道每探针间隔一次 GET，零货币成本；受全局与渠道级开关约束，与既有纪律一致。
- [配置包新增 `health` 依赖] → 仅用于能力判定，无循环依赖；若不接受该方向，可把能力函数下沉到 adapter 包，但当前无必要。

## Migration Plan

无数据迁移。发布后 Gemini 原生渠道默认开始免费探针；需要静默的渠道显式声明 `free_probe: false`。回滚只需关闭全局 `health.free_probe_enabled` 或移除 gemini 表项。
