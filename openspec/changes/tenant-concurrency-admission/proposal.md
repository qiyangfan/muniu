# Proposal: tenant-concurrency-admission（租户并发准入）

> 优先级：P2（治理层缺口；不属于 key 池/failover/熔断层）。关联 ADR：`docs/adr/0028-tenant-concurrency-admission.md`。

## Why

多租户目前只有 RPM/TPM/日配额，三者都是速率或事后记账：TPM 只统计已完成请求，长流式请求在完成前不占额度；代码里也没有 per-tenant 在途并发闸。一个租户可以同时挂住大量长连接，占满上游 key 池与本地连接，其他租户被挤占。这是流量治理层的缺口，不是 key 池/failover/熔断层的调度问题。

## What Changes

- 新增租户级在途并发上限：同一租户跨 lane（chat/messages/responses/gemini/embeddings/images/multimodal）共享一个在途计数。
- 准入点在各 lane 进入候选循环/上游调用之前；超限请求返回 429 `tenant_inflight_exceeded`，不触上游、不落 usage 行。
- 槽位在所有出口释放（成功、失败、客户端断连、预算中断、panic 隔离路径）。
- 配置：`tenant_default_max_inflight`（缺省 0=不限）与 standalone `tenants[].max_inflight` 覆盖；managed 租户快照暂无该字段，v1 使用本地缺省值，控制面字段另立跨仓变更。
- 观测：`muniu_tenant_inflight` 水位与 `muniu_tenant_admission_denials_total{tenant}`；拒绝日志带 tenant/request_id。
- 本变更是对外契约变更（新增拒绝行为），实施前需确认；分布式共享计数不在本变更范围（每副本独立，上限按副本计）。

## Capabilities

### New Capabilities

无

### Modified Capabilities

- `traffic-governance`：租户级在途并发准入与释放语义。

## Impact

- 代码：`internal/proxy` 各 lane 入口与出口、`internal/ratelimit` 或新租户计数器、`internal/config`、`internal/metrics`。
- 行为：默认 0 保持现状；配置上限后新增 429 拒绝；多副本时总在途上限为副本数 × 配置值。
- 测试：A 占满、A 拒绝、B 不受影响；槽位释放；断连/预算中断释放；0=不限；managed 使用缺省。
- 依赖：零新增依赖；无存储迁移。
