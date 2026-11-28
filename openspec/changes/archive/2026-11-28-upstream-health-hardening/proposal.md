# Proposal: upstream-health-hardening（健康熔断覆盖面与半开纪律）

> 优先级：P2（lane 覆盖 + 半开并发），并含 P3 被动参数拆分。关联 ADR：`docs/adr/0019-upstream-health-circuit-breaking.md`（2026-11-27 修订）。

## Why

渠道级熔断目前只在 chat 家族候选循环里生效；embeddings、images、multimodal 各自有候选循环，responses 状态查询也有独立取 key 路径，它们都不查熔断态、也不回填被动统计。同一渠道 chat 已熔断，向量/图片流量仍会打过去。另有两处纪律缺口：半开期所有并发请求都去试探，恢复中的渠道会吃一整波流量；被动窗口与换道率告警共用 `switch_*` 参数，告警灵敏度与熔断灵敏度无法分开调。

## What Changes

- 健康收缩与被动/半开回填覆盖全部数据面候选循环：chat/messages/responses/gemini、embeddings、images、multimodal；responses 状态查询至少查询熔断态并快速失败（该路径无法换道）。
- 半开期增加并发试探上限 `health.half_open_max_inflight`（缺省 1，0=不限）；超额请求 503 `provider_circuit_open`，不触上游；试探结果释放名额并裁决恢复/回降。
- 被动统计改用独立参数 `health.passive_window_sec` / `passive_min_samples` / `passive_rate_milli`，缺省 300/20/300 与现状等值；不再与换道率告警共用阈值。
- 补跨 lane 一致性测试、半开并发测试、独立参数测试。

## Capabilities

### New Capabilities

无

### Modified Capabilities

- `upstream-health`：熔断覆盖全部数据面 lane；半开并发试探上限；被动统计独立参数。

## Impact

- 代码：`internal/proxy` 各候选循环、`internal/health` 半开名额与配置、`internal/config` 校验、装配层。
- 行为：非 chat lane 开始受渠道熔断约束；半开期默认只放 1 个试探请求；新增配置缺省保持被动阈值不变。
- 测试：每 lane × 渠道相位（healthy/degraded/half-open）、半开并发、参数隔离。
- 依赖：零新增依赖；无存储迁移。
