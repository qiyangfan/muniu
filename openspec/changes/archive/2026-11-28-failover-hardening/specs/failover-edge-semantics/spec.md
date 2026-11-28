# Spec Delta

## Purpose

failover 边界语义：Retry-After 的合法形态解析，以及 key 池全不可用时给客户端准确的 429/503 信封与最早恢复提示。

## ADDED Requirements

### Requirement: Retry-After 两种合法形态

上游 429 的 `Retry-After` 必须 (MUST) 支持 delta-seconds 与 HTTP-date 两种形态;HTTP-date 必须 (MUST) 转换为到当前时刻的时长;缺失、非法或过去时刻必须 (MUST) 回落既有指数阶梯,不得 (MUST NOT) 把无效值当成 0 秒冷却立即重试。

#### Scenario: 秒数与日期形态

- **WHEN** 上游返回 `Retry-After: 60` 或 `Retry-After: Wed, 21 Oct 2015 07:28:00 GMT`
- **THEN** key 冷却分别按 60 秒与该日期到当前的时长执行

#### Scenario: 非法值回落阶梯

- **WHEN** `Retry-After` 为非法字符串或已过去的日期
- **THEN** 使用既有 60 秒 × 2^(连续次数-1) 阶梯,封顶 30 分钟

### Requirement: key 池耗尽分级信封

全部候选均因 key 池不可用而跳过且无真实上游错误时,网关必须 (MUST) 按池状态分级响应:全部为限频/欠费冷却 → 429 `keypool_rate_limited` 并携带最早恢复 `Retry-After`;存在禁用 key 或混合原因 → 503 `keypool_exhausted`(有计时冷却时携带 `Retry-After`);不得 (MUST NOT) 返回 502 `all_candidates_failed` 空错误类,不得 (MUST NOT) 发起上游调用或落 usage 行。

#### Scenario: 全 key 限频冷却

- **WHEN** 别名所有 key 均在 429 冷却中,最早 60 秒后恢复
- **THEN** 返回 429 `keypool_rate_limited`,`Retry-After: 60`,上游零调用

#### Scenario: key 被禁用

- **WHEN** 别名所有 key 均因 401 禁用
- **THEN** 返回 503 `keypool_exhausted`,不携带 `Retry-After`

#### Scenario: 混合原因

- **WHEN** 部分 key 冷却、部分禁用
- **THEN** 返回 503 `keypool_exhausted`,若有计时冷却则携带最早恢复 `Retry-After`

#### Scenario: 非池原因不冒认

- **WHEN** 候选因健康熔断、oauth lane 或桥接适配性被跳过
- **THEN** 维持既有具体信封或 `all_candidates_failed`,不走 key 池耗尽分级
