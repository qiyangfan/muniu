# Design: upstream-health-hardening

## Context

`Contracted` 只在 `handleChat` 的候选循环调用（[proxy.go]）；embeddings/images/multimodal 各自复制了候选循环；responses 状态查询是单 provider 取 key。半开期 `Contracted=false`，所有流量都当试探。`providerWins` 与 `aliasWins` 复用同一组 `SwitchWindow/SwitchMinSamples/SwitchRateMilli`。

## Goals / Non-Goals

**Goals:**

- 任何数据面出站路径都不会绕过渠道熔断。
- 半开恢复信号可控，不被并发洪峰淹没。
- 告警阈值与熔断阈值解耦，缺省行为不变。

**Non-Goals:**

- 不实现按模型 lane 的健康（维持渠道级）。
- 不改变探测端点、状态机相位与同层恢复判据。
- 不改变各 lane 的 failover 判定表。

## Decisions

1. **共享候选门卫而非逐 lane 复制**：在 proxy 内抽一个 `healthGate(cand)` 小函数（或复用 `skipChatCandidate` 的健康分支），各 lane 的候选循环先调用它；回填统一走 `ProviderTraffic/TrialResult`。避免第五份分支。备选是逐 lane 复制三行判断，长期会漏接，否决。
2. **状态查询 lane 只收缩不换道**：该路径按亲和钉在单 provider，无法换道；熔断期直接 503 `provider_circuit_open`，与单候选快速失败同族。
3. **半开名额放在 health.Manager**：每渠道一个计数（加锁或原子），`TrialAcquire(name)` / `TrialRelease(name)`；`Acquire` 仅在 half-open 生效，超过 `HalfOpenMaxInflight` 返回 false；请求终结必须释放。成功/失败仍由现有 `TrialResult` 裁决。备选是 proxy 侧信号量，跨 lane 重复，否决。
4. **独立被动参数**：`passive_*` 缺省回落到 300/20/300（与当前 `switch_*` 等值），配置校验非负；`switch_*` 只服务换道率告警。这样默认行为逐字节不变，调参互不影响。
5. **lane 覆盖范围**：所有实际发起候选 attempt 的循环；responses 状态查询作为只读路径单独判收缩。跳过的候选不计错误类、不计换道率。

## Risks / Trade-offs

- [非 chat lane 行为变化] → 这是缺陷修复；golden 测试按 lane 断言，避免误跳过健康渠道。
- [半开默认只放 1 个请求] → 恢复变慢；配置可调，且比打垮恢复中渠道更安全。
- [名额泄漏导致永久半开卡死] → 请求全出口 `defer Release`；测试覆盖断连/预算中断/panic 隔离路径。
- [新参数与旧参数并存] → 文档明确 `switch_*` 不再控制熔断；配置校验防止 0/负值歧义。

## Migration Plan

无存储迁移。新配置缺省等值，滚动升级不改变行为；lane 覆盖与半开上限是行为变更，发布说明标注。回滚可设 `half_open_max_inflight: 0` 或关闭 `health.enabled`。
