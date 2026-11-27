# Spec Delta

## ADDED Requirements

### Requirement: 控制面用量与健康上报

muniu SHALL 以服务身份向锦官推送 LLM usage 与 health 事件，带 source=muniu、租户/key/模型维度与幂等键；LLM token/费用 SHALL 以 muniu 为唯一来源；锦官不可用 SHALL 本地缓冲并重试；SHALL NOT 上报请求内容或凭据。

#### Scenario: 用量上报

- WHEN 请求完成计量
- THEN usage 事件发送

#### Scenario: 断网缓冲

- GIVEN 锦官不可用
- WHEN 产生用量
- THEN 本地缓冲，恢复补发

#### Scenario: 服务认证

- WHEN 无服务身份上报
- THEN 401/403，不落库
