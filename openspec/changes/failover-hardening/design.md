# Design: failover-hardening

## Context

候选循环逐 attempt 起拨号/首 token 计时，没有任何阶段总预算；key 池全不可用时由 `finishChatExhausted` 统一写 502。`parseRetryAfter` 仅 `strconv.Atoi` 秒数。见 proposal.md「Why」。

## Goals / Non-Goals

**Goals:**

- 首 token 前的换道链有确定上界，客户端不会被 N×65 秒拖死。
- 池耗尽信封能表达「限频冷却」与「不可用」，并给出最早恢复提示。
- Retry-After 按 RFC 语法解析。

**Non-Goals:**

- 不改变首 token 后断言、断流处理与流式转发。
- 不引入并行对冲/hedging。
- 不做分布式预算或跨副本协调（每实例独立）。

## Decisions

1. **预算是「首 token 前」的，不是整请求总超时**。候选循环入口记录 deadline；每次 attempt 前检查 `max_attempts` 与 deadline；`m.firstSet`（首 token 已提交）后预算失效。这样长回答不受影响，保持三层超时模型。
2. **默认 120 秒 / 4 次，0=不限**。120 秒约等于两个首 token 超时，覆盖常见 2–3 候选；4 次覆盖正常路由长度。备选是默认关闭（0），但那等于不修缺口；备选是只设 deadline 不设次数，候选多且快速失败时仍可能打满。两者都设，显式 0 可回退。
3. **超限信封 504 `failover_budget_exhausted`**，message 携带最后错误类与已完成尝试数；不触上游、不新增 usage 行（已发生的 attempt 各自计量）。若已提交首 token，则按现状断言出口，绝不因预算改写已输出响应。
4. **池耗尽用只读可用性视图**：`keypool.Availability(now)` 返回是否有可用 key、最早恢复时刻、是否全部为限频/欠费冷却。候选循环记录跳过原因；全部候选均因池不可用而跳过时，按该视图分级写 429/503；混合了熔断/oauth/桥接跳过时仍走既有具体信封或 `all_candidates_failed`。
5. **Retry-After 解析**：先按整数秒；失败则按 `http.ParseTime` 解析 HTTP-date，转成到当前的时长；过去时刻/非法/缺失返回 0，由既有指数阶梯兜底。保留 0=未提供语义。
6. **本变更是契约级**：新增配置、默认行为变化、错误码变化；实施前补 ADR，更新设计文档与 OpenAPI/错误码清单。

## Risks / Trade-offs

- [默认预算改变多候选行为] → 发布说明标注；`0` 可完全回退。
- [504 新错误码客户端未识别] → 沿用错误信封结构，message 自解释；文档同步。
- [池耗尽分级误判] → 只有「全部跳过原因均为池不可用」才走新信封；混合原因保持现有语义。
- [HTTP-date 时区/格式差异] → 只用 `http.ParseTime`，测试覆盖 IMF-fixdate/RFC850/asctime 与非法串。

## Migration Plan

无存储迁移。配置缺省即生效；灰度期可显式 `failover_deadline_sec: 0`、`max_attempts: 0` 保持旧行为。回滚为配置回退，无数据影响。
