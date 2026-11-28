# Spec Delta

## ADDED Requirements

### Requirement: 请求级错误不进入 key 健康度

key 健康度与冷却必须 (MUST) 只消费 key 级信号:429/401/403/欠费更新冷却、禁用与 EMA;5xx 保留连续计数冷却;400/404/未知 4xx/客户端断连/预算中断不得 (MUST NOT) 改变 EMA、冷却或禁用。分类以 `error-evidence-scope` 判定表为唯一源。

#### Scenario: 参数错误不动 key

- **WHEN** 上游返回 400 参数错误或 404 候选模型不存在
- **THEN** 该 key 的 EMA、连续计数、冷却与禁用状态均不变

#### Scenario: 限频仍冷却 key

- **WHEN** 上游返回 429
- **THEN** 该 key 按既有 Retry-After/阶梯语义进入冷却,EMA 记录失败

#### Scenario: 客户端断连不记失败

- **WHEN** 客户端在响应完成前断连
- **THEN** 该 key 不记录 EMA 失败,不进入冷却
