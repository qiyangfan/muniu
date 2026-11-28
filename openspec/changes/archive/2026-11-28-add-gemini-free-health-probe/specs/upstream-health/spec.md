# Spec Delta

## ADDED Requirements

### Requirement: Gemini 原生免费控制面探针

Gemini 原生适配器渠道必须 (MUST) 默认获得免费控制面探针:按探针间隔请求 `GET {base_url}/models`(Google models.list),以 `x-goog-api-key` 鉴权,零货币成本;全局 `health.free_probe_enabled` 与渠道级 `providers[].health.free_probe: false` 的退订语义与其他适配器一致;探针结果零触碰 key 池状态(EMA/冷却/禁用)。

#### Scenario: Gemini 静默渠道控制面死亡被发现

- **WHEN** 某 gemini 渠道数小时无真实流量,上游控制面故障(域名不可达/服务宕/密钥被吊销)
- **THEN** 免费探针按间隔发起 `GET {base_url}/models`,连续失败达阈值后渠道进入降级态并发射 `provider_degraded` 告警,早于任何真实用户请求,全程零货币成本

#### Scenario: Gemini 探针鉴权形态

- **WHEN** gemini 渠道的免费探针出站
- **THEN** 请求携带 `x-goog-api-key` 头,不携带其他适配器族的鉴权头

#### Scenario: Gemini 渠道退订零探针

- **WHEN** gemini 渠道声明 `providers[].health.free_probe: false`
- **THEN** 该渠道零探针请求,被动统计照常工作

### Requirement: Gemini 推理探针声明拒绝

Gemini 渠道声明 `providers[].health.probe_model` 必须 (MUST) 在配置校验期拒绝并明确报错,不得静默忽略或降级为免费探针;推理探针启用范围仅限已声明推理端点形态的适配器。

#### Scenario: Gemini 声明 probe_model 被拒绝

- **WHEN** 某 gemini 渠道持有 key 并声明 `health.probe_model: gemini-pro`
- **THEN** 配置加载失败,错误信息指明该渠道与 `probe_model` 不受支持
