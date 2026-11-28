# Proposal: failover-hardening（换道预算与边界语义）

> 优先级：P2（首 token 前总预算），并含 P3 边缘语义（Retry-After / 池耗尽信封）。关联 ADR：`docs/adr/0026-failover-budget-and-exhaustion-envelope.md`。

## Why

failover 只有三层单次超时，没有首 token 前的阶段总预算：N 个候选各 60 秒，客户端可能被拖到 N×65 秒，早已断连而网关仍在换道。另有两处边界不精确：`Retry-After` 只识别整数秒，HTTP-date 形态一律回落 60 秒阶梯；key 池全冷却时最终信封是 502 `all_candidates_failed` 且最后错误类为空，客户端拿不到 429/503 与 `Retry-After`。

## What Changes

- 新增首 token 前阶段预算：`failover_deadline_sec`（缺省 120）与 `max_attempts`（缺省 4），仅统计真实上游尝试，跳过候选不计；超限返回 504 `failover_budget_exhausted`，不再发起上游调用；首 token 已提交后的长响应不受影响；显式 0 = 不限。
- `Retry-After` 解析支持 delta-seconds 与 HTTP-date 两种合法形态；缺失/非法/过去时刻回落既有指数阶梯。
- key 池耗尽信封分级：全部因限频/欠费冷却 → 429 `keypool_rate_limited` + 最早恢复 `Retry-After`；存在禁用或混合原因 → 503 `keypool_exhausted` + 可用时的 `Retry-After`；不再落 502 空错误类。
- 更新 failover 判定表、错误信封文档与测试矩阵。

## Capabilities

### New Capabilities

- `failover-deadline`：首 token 前的换道总时长与尝试次数上界。
- `failover-edge-semantics`：Retry-After 解析与 key 池耗尽信封。

### Modified Capabilities

无

## Impact

- 代码：`internal/proxy` 候选循环、`internal/keypool` 只读可用性视图、`internal/proxy` Retry-After 解析、错误信封映射。
- 行为：缺省即新增预算（多候选/长挂起请求行为变化）；池耗尽状态码与错误码变化；契约级，实施前补 ADR 并确认。
- 测试：候选挂起超预算、首 token 后不截断、0=不限、Retry-After 三形态、池耗尽 429/503 分支。
- 依赖：零新增依赖；无存储迁移。
