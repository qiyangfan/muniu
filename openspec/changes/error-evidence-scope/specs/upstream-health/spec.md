# Spec Delta

## ADDED Requirements

### Requirement: 被动统计只消费渠道级证据

渠道被动失败窗口与半开试探裁决必须 (MUST) 只消费渠道级证据:429/401/403/欠费不得计入;dial/TLS/首 token 超时/5xx/首 token 前断流计入;400/404 与客户端断连、预算中断不计入。证据分类以 `error-evidence-scope` 判定表为唯一源。

#### Scenario: 429 风暴不降渠道

- **WHEN** 单渠道多把 key 轮流返回 429,期间持续有请求成功
- **THEN** 渠道被动窗口不计入这些 429,渠道相位保持 healthy

#### Scenario: 5xx 触发被动降级

- **WHEN** 渠道真实流量窗口内 5xx 失败率达到阈值且样本充足
- **THEN** 渠道进入被动降级,与既有阈值语义一致

#### Scenario: 参数错误不计失败

- **WHEN** 渠道返回 400 或 404
- **THEN** 被动窗口 total 与 failure 均不增加

### Requirement: 半开试探只由渠道级成败裁决

半开期真实请求必须 (MUST) 仅在渠道级成功或渠道级失败时裁决恢复/回降;key 级限频、鉴权失败与请求级参数错误不得 (MUST NOT) 使渠道回降级。

#### Scenario: 半开期 key 限频不误判

- **WHEN** 渠道处于 half-open,真实请求因单 key 429 换道后成功
- **THEN** 渠道按成功试探恢复,不因 429 回降级
