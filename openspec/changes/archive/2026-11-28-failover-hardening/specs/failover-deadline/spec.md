# Spec Delta

## Purpose

首 token 前的换道阶段预算：用最大尝试数与总时长给候选链设上界，避免客户端被 N×首 token 超时拖死，同时不误伤已开始输出的长回答。

## ADDED Requirements

### Requirement: 首 token 前阶段预算

网关必须 (MUST) 对首 token 提交前的候选尝试执行 `failover_deadline_sec`(缺省 120)与 `max_attempts`(缺省 4)双重上界;只有真实上游尝试计入次数,跳过候选不计;任一上界命中必须 (MUST) 停止换道并返回 504 `failover_budget_exhausted`,不得 (MUST NOT) 再发起上游调用;首 token 已提交后预算失效,长响应不得 (MUST NOT) 被截断。

#### Scenario: 候选悬挂命中总预算

- **WHEN** 别名有 5 个候选,每个都在首 token 前挂满超时
- **THEN** 网关在 120 秒(或第 4 次尝试)时停止换道,返回 504 `failover_budget_exhausted`;第 5 个候选上游零调用

#### Scenario: 首 token 后不受预算影响

- **WHEN** 首 token 在预算内提交,响应持续 10 分钟
- **THEN** 预算不再生效,流式响应完整转发

#### Scenario: 显式关闭预算

- **WHEN** `failover_deadline_sec: 0` 且 `max_attempts: 0`
- **THEN** 行为回退为逐候选尝试至耗尽,与变更前一致

#### Scenario: 跳过候选不计数

- **WHEN** 前两个候选因熔断或池不可用被跳过,第三个候选真实请求失败
- **THEN** `max_attempts` 只计入第三个候选的一次真实尝试

#### Scenario: 非 chat lane 同口径

- **WHEN** embeddings/images/multimodal 候选链在首 token 前耗尽尝试次数或 key 池
- **THEN** 与 chat 一样返回 504 `failover_budget_exhausted` 或 429/503 池耗尽信封,不触后续上游
