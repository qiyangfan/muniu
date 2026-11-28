# Spec Delta

## ADDED Requirements

### Requirement: 探针取样避开不可用 key

探针必须 (MUST) 从池内选择非禁用且不在冷却期的 key 作为取样目标,并按渠道轮换;不得 (MUST NOT) 调用 `Pick/Report` 或写入 EMA、冷却、禁用状态。池内无可用 key 时探针必须 (MUST) 跳过本轮,不得 (MUST NOT) 计入失败或推进降级。

#### Scenario: 首键撤销不再熔断渠道

- **WHEN** 渠道首把 key 因 401 被禁用,其余 key 健康
- **THEN** 免费探针使用其余健康 key,渠道相位保持 healthy

#### Scenario: 首键冷却不污染推理探针

- **WHEN** 渠道首把 key 处于欠费冷却,其余 key 可用
- **THEN** 推理探针不选择该冷却 key,也不因它的 402 判渠道降级

#### Scenario: 全部不可用跳过探针

- **WHEN** 渠道全部 key 均禁用或冷却
- **THEN** 本轮不发起探针请求,不增加连续失败计数;数据面按池耗尽语义处理

#### Scenario: 轮换覆盖多 key

- **WHEN** 渠道有两把健康 key 且连续多轮探针
- **THEN** 探针取样在 key 间轮换,不固定首键
