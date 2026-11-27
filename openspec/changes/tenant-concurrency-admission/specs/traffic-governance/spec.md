# Spec Delta

## ADDED Requirements

### Requirement: 租户在途并发准入

每个租户必须 (MUST) 受跨 lane 的在途并发上限约束:同一租户的 chat/messages/responses/gemini/embeddings/images/multimodal 上游调用共享一个在途计数;达到上限时新请求必须 (MUST) 在上游调用前返回 429 `tenant_inflight_exceeded`,不得 (MUST NOT) 触上游或落 usage 行;槽位必须在所有出口释放(成功/失败/断连/预算中断);上限 0=不限。

#### Scenario: 单租户占满不影响他人

- **WHEN** 租户 A 达到在途上限,租户 B 发起请求
- **THEN** A 新请求 429 `tenant_inflight_exceeded`,B 正常服务

#### Scenario: 槽位释放后恢复

- **WHEN** A 的在途请求完成或断连,在途数降到上限以下
- **THEN** A 的下一请求正常准入

#### Scenario: 断连释放

- **WHEN** A 的长流式请求客户端断连
- **THEN** 槽位立即释放,不遗留占位

#### Scenario: 默认不启用

- **WHEN** 未配置任何租户在途上限
- **THEN** 准入恒放行,行为与本变更前一致

### Requirement: 租户并发配置与观测

租户在途上限必须 (MUST) 支持 `tenant_default_max_inflight` 全局缺省(0=不限)与 standalone `tenants[].max_inflight` 覆盖;managed 租户无快照字段时必须 (MUST) 使用本地缺省;网关必须 (MUST) 暴露在途水位与拒绝计数指标,并在拒绝日志中携带 tenant 与 request_id。

#### Scenario: managed 使用本地缺省

- **WHEN** managed 租户快照不含并发字段,本地缺省为 8
- **THEN** 该租户按 8 执行准入

#### Scenario: 拒绝可观测

- **WHEN** 租户因在途超限被拒绝
- **THEN** `muniu_tenant_admission_denials_total{tenant}` 递增,日志含 tenant 与 request_id
