# Spec Delta

## ADDED Requirements

### Requirement: 熔断覆盖全部数据面 lane

所有实际发起上游候选尝试的数据面路径(chat/messages/responses/gemini、embeddings、images、multimodal)必须 (MUST) 在候选级查询渠道熔断态并跳过降级渠道,且把渠道级成败回填被动窗口与半开试探;responses 状态查询路径必须 (MUST) 在熔断期快速失败 503 `provider_circuit_open`(该路径无法换道)。跳过的候选不得 (MUST NOT) 计入错误类或换道率。

#### Scenario: 非 chat lane 跳过熔断渠道

- **WHEN** 某渠道在 chat lane 已进入 degraded,embeddings/images/multimodal 请求的别名包含该渠道
- **THEN** 这些 lane 的候选循环跳过该渠道,上游零调用,用户由其余候选服务

#### Scenario: 半开试探跨 lane 生效

- **WHEN** 渠道处于 half-open,首个真实请求来自 embeddings lane 并成功
- **THEN** 渠道按成功试探恢复,chat lane 随后恢复使用

#### Scenario: 状态查询快速失败

- **WHEN** responses 状态查询的目标渠道处于 degraded
- **THEN** 返回 503 `provider_circuit_open`,不发起上游查询

### Requirement: 半开期并发试探上限

半开期必须 (MUST) 限制同时在途的真实试探请求数为 `health.half_open_max_inflight`(缺省 1,0=不限);超额请求必须 (MUST) 快速失败 503 `provider_circuit_open` 且不触上游;试探成功或失败必须 (MUST) 释放名额并裁决恢复或回降级。

#### Scenario: 半开并发只放一个试探

- **WHEN** 渠道 half-open,同时到达 10 个请求,上限为 1
- **THEN** 仅 1 个请求打到上游,其余 9 个 503;上游零调用

#### Scenario: 试探成功恢复

- **WHEN** 该唯一试探请求成功
- **THEN** 渠道恢复 healthy,后续请求正常放行

#### Scenario: 名额释放

- **WHEN** 试探请求因断连或预算中断终结
- **THEN** 名额释放,后续半开请求可再次试探

### Requirement: 被动统计独立参数

渠道被动失败窗口必须 (MUST) 使用独立的 `health.passive_window_sec`、`passive_min_samples`、`passive_rate_milli`(缺省 300/20/300),不得 (MUST NOT) 与换道率告警的 `switch_*` 参数共用;修改告警阈值不得改变被动熔断判定。

#### Scenario: 告警参数不影响熔断

- **WHEN** 将 `switch_rate_milli` 调整为 100,`passive_rate_milli` 保持 300
- **THEN** 换道率告警按 10% 触发,被动熔断仍按 30% 判定

#### Scenario: 缺省等值

- **WHEN** 配置未声明任何 `passive_*` 字段
- **THEN** 被动窗口按 300 秒、20 样本、30% 运行,与变更前一致
