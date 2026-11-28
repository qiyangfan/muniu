# Spec Delta

## Purpose

上游错误证据的作用域归属：同一个 `ErrorClass` 只允许被指定的消费者（key 池、渠道健康、failover 尝试控制）读取，避免 key 级限频熔断整渠道、请求级参数错误拉低 key 健康度。

## ADDED Requirements

### Requirement: 错误证据作用域判定表

每个上游错误类必须 (MUST) 映射为 `keySignal`、`channelSignal`、`attemptAction` 三列：key 池只消费 key 级信号,渠道健康只消费渠道级信号,请求/候选级错误不得 (MUST NOT) 改变 key 健康或渠道相位。无信号列必须 (MUST) 明确为 none,禁止默认推断。

#### Scenario: 限频不熔断渠道

- **WHEN** 单把 key 连续返回 429,同渠道其他 key 正常
- **THEN** 仅该 key 进入冷却;渠道健康相位不变,不发射 `provider_degraded`

#### Scenario: 参数错误不拉低 key

- **WHEN** 上游返回 400 参数错误,或 404 候选模型不存在
- **THEN** key 的 EMA、冷却与禁用状态均不变,渠道被动窗口不计入失败

#### Scenario: 渠道错误仍熔断

- **WHEN** 上游连续返回 5xx,或拨号/首 token 超时
- **THEN** 渠道被动窗口计入失败并按既有阈值降级;key 级 5xx 连续计数冷却保留

#### Scenario: 客户端断连与预算静默

- **WHEN** 客户端断连或请求级成本预算中断
- **THEN** 不换道、不计 key 失败、不计渠道失败

### Requirement: 同 provider 换 key 重试

key 级硬错误(429/401/403/欠费)必须 (MUST) 允许在同一 provider 内换用池中下一可用 key 重试,直到池耗尽或 failover 预算用尽;该重试不得 (MUST NOT) 计入别名换道率或渠道被动失败窗口。渠道级错误必须 (MUST) 直接切换下一候选,不得 (MUST NOT) 在同 provider 内逐个换 key。

#### Scenario: 同渠道换 key 成功

- **WHEN** 别名只配置一个 provider,池内 key A 返回 429 且 key B 健康
- **THEN** 请求换 key B 重试并成功,别名换道率与渠道被动窗口均不增加

#### Scenario: 池耗尽后才换候选

- **WHEN** 同 provider 全部 key 均冷却/禁用,仍有下一候选
- **THEN** 进入下一候选;最终信封如实说明已尝试全部候选

#### Scenario: 渠道错误不换 key

- **WHEN** key A 返回 5xx
- **THEN** 直接切换下一候选,不使用同 provider 的 key B
