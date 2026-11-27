# ADR-0028:租户并发准入——速率配额之外的第二种隔离

- 状态:已接受(2026-11-27;openspec `tenant-concurrency-admission` 承载)
- 背景:多租户只有 RPM/TPM/日配额,三者都是速率或事后记账;TPM 只统计已完成请求,长流式请求在完成前不占额度;代码内没有 per-tenant 在途并发闸。一个租户可以挂住大量长连接,占满上游 key 池与本地连接,其他租户被挤占。
- 关联:ADR-0008(集群限流语义)、openspec `traffic-governance`

## 决策

1. **租户级、跨 lane 的在途计数**:同一租户的 chat/messages/responses/gemini/embeddings/images/multimodal 上游调用共享一个计数器;不按 lane 或 provider 拆分。
2. **准入点**:语义缓存之后、候选循环/上游调用之前;缓存命中不占名额,被限制的是上游并发。
3. **拒绝语义**:超限返回 429 `tenant_inflight_exceeded`,不触上游、不落 usage 行;不设置 `Retry-After`——槽位释放时间取决于在途请求时长,固定秒数是伪精确。
4. **释放纪律**:准入后全生命周期 `defer Release`,覆盖成功、失败、客户端断连、预算中断与 panic 隔离路径;泄漏由水位指标与测试兜底。
5. **配置**:`tenant_default_max_inflight` 全局缺省(0=不限),standalone `tenants[].max_inflight` 可覆盖;managed 租户快照暂无该字段时使用本地缺省;控制面字段另立跨仓变更。默认 0 保持现有部署行为。
6. **多副本**:每副本独立计数,总在途 ≤ 副本数 × 配置值;不做分布式共享计数(与 ADR-0008 的降级纪律一致)。
7. **观测**:`muniu_tenant_inflight` 水位与 `muniu_tenant_admission_denials_total{tenant}`;拒绝日志携带 tenant 与 request_id。

## 备选与否决

- 只靠 RPM/TPM:TPM 完成后记账,RPM 不约束长连接并发,堵不住 noisy neighbor——否决。
- 拒绝用 503:会把「租户超限」说成「上游不可用」,误导客户端——否决。
- 按 lane/provider 拆分计数:单租户仍可在多个 lane 叠加占满,隔离不成立——否决。
- v1 就做跨副本共享计数:引入 Redis 依赖与降级语义,超出当前缺口——否决,记 backlog。

## 后果

- 新对外拒绝行为,实施前需确认 429 契约;默认 0 时行为不变。
- example 与 traffic-governance 文档同步;生产多租户应显式配置上限。
- 测试覆盖:A 占满、A 拒绝、B 不受影响;槽位释放;断连/预算中断释放;0=不限;managed 使用本地缺省。
- 回滚:上限设为 0,无数据迁移。
