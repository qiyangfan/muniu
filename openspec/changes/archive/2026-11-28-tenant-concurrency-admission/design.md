# Design: tenant-concurrency-admission

## Context

租户配额在限流阶段执行，维度是 RPM/TPM/日配额；TPM 为完成后记账，长流式请求在完成前不占额度。数据面各 lane 独立处理，没有统一的租户在途计数。见 proposal.md「Why」。

## Goals / Non-Goals

**Goals:**

- 租户级、跨 lane 的在途并发上界，防止单租户占满长连接。
- 准入/释放语义可判定、可测试，且不触上游。
- 默认关闭，保持现有部署行为。

**Non-Goals:**

- 不做跨副本共享计数（per-replica，文档明确）。
- 不改变 RPM/TPM/日配额语义。
- 不新增控制面 wire 字段；managed 上限复用 `entitlements.quotas` 的 `max_inflight` 维度。
- 不做按模型/按 provider 的并发隔离。

## Decisions

1. **在途计数放在租户维度，共享一个计数器**：所有 lane 的候选循环/上游调用前 `Acquire(tenant)`，请求终结 `Release`。理由：noisy neighbor 是租户级问题，按 lane 分会漏；缓存命中不触上游，不占名额。
2. **准入点在语义缓存之后、候选循环之前**：chat 在 cache lookup 之后；embeddings/images/multimodal 在取 key 前；responses 状态查询在出站前。这样缓存命中不受并发闸影响，上游并发才是被限制的对象。
3. **拒绝语义 429 `tenant_inflight_exceeded`，无 `Retry-After`**：槽位释放时间取决于在途请求时长，给固定秒数是伪精确；message 提示稍后重试。备选 503 会把「租户超限」说成「上游不可用」，否决。
4. **配置**：`tenant_default_max_inflight`（0=不限）为全局缺省，`tenant_max_inflight` 覆盖表按租户 ID 覆盖；managed 租户由快照 `entitlements.quotas` 的 `max_inflight` 维度下发，缺席回落本地缺省，快照替换时 managed 覆盖整体撤销。
5. **释放纪律**：每个 lane 用 `defer Release` 包住准入后的整个生命周期；panic 隔离路径也必须释放（defer 天然覆盖）。计数用互斥；泄露由测试与水位指标兜底。
6. **多副本**：每副本独立计数，总在途 ≤ 副本数 × 上限；文档明确，不做分布式强一致（与 ADR-0008 的降级纪律一致）。

## Risks / Trade-offs

- [默认 0 不解决开箱即用] → 文档与 example 标注生产多租户应配置；上限值需按容量设定，避免拍脑袋默认。
- [槽位泄漏] → defer 释放 + 水位指标 + 断连/预算中断测试。
- [长请求占满槽位] → 这正是保护目标；运维可通过水位与拒绝指标调整上限。
- [多副本总上限放大] → 文档明确；共享计数列入 backlog，不在本变更。

## Migration Plan

无存储迁移。默认 0 灰度不改变行为；配置上限后新拒绝路径生效。回滚把上限设为 0，无需数据操作。
