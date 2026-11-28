# Proposal: error-evidence-scope（上游错误证据作用域）

> 优先级：P1（key/渠道/请求级证据错配 + 同 provider 多 key 无请求内重试）。关联 ADR：`docs/adr/0027-error-evidence-scope.md`。

## Why

当前三类消费者对同一错误各判各的：429/401 会进入渠道被动窗口与半开裁决（key 级证据熔断整渠道），400/404 会进入 key EMA（请求/候选级证据拉低 key 健康度）；同时同一 provider 有多把 key 时，一次 429 只会换下一个 provider，不会换同 provider 的其它健康 key。根因是缺少一张统一的「错误类 → 证据作用域 → 消费者动作」判定表。

## What Changes

- 新增错误证据作用域判定表：每个 `ErrorClass` 明确 `keypool` 信号、`health` 信号与 `failover` 尝试动作（换同 provider key / 换下一候选 / 直接返回）。
- key 级错误（429/401/403/欠费）只更新 key 池，并允许在同一 provider 内换下一把健康 key 重试（有界，受 failover 预算约束）；不得计入别名换道率或渠道被动失败窗口。
- 渠道级错误（dial/TLS/首 token 超时/5xx/首 token 前断流）进入渠道健康与下一候选；5xx 保留 key 连续计数冷却，dial/超时保留 key EMA 软信号（不触发冷却）。
- 请求/候选级错误（400 参数、404 模型不存在、未知 4xx、客户端断连、预算策略）不改变 key 健康，也不改变渠道健康；400 维持不换道，404/未知按候选级换道。
- 更新 failover 判定表与 keypool/health 消费点；本变更是契约级（尝试序列可观测变化），实施前须补 ADR。

## Capabilities

### New Capabilities

- `error-evidence-scope`：上游错误类到 key/渠道/请求作用域的归属规则，以及各消费者（keypool、health、failover）允许消费的信号。

### Modified Capabilities

- `upstream-health`：被动统计与半开试探只消费渠道级证据。
- `keypool-scheduling`：请求级/候选级错误不进入 key 健康度；key 级错误保持 key 池语义。

## Impact

- 代码：`internal/failover`、`internal/proxy`（候选循环）、`internal/keypool` 结果上报、`internal/health` 被动入口。
- 行为：同 provider 换 key 重试会增加尝试次数与部分延迟；429 不再单独熔断渠道；400/404 不再拉低 key EMA；判定表契约变化需 ADR。
- 测试：判定表全格（ErrorClass × 消费者 × 首 token 状态）、多 key 同 provider 重试序列、429 风暴不降渠道、400/404 不动 key。
- 依赖：零新增依赖；无存储迁移。
