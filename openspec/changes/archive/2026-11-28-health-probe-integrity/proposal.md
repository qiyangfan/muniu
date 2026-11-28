# Proposal: health-probe-integrity（探针取样完整性）

> 优先级：P1（单键撤销可永久熔断渠道）。

## Why

免费与推理探针都固定取池内第一把非空 key，且快照包含已禁用 key。第一把 key 被上游 401 撤销后，数据面已禁用该 key，探针仍持续用它拿到 401，连续失败把整个渠道判降级；在该 key 被移除前渠道无法自愈。冷却中的 key 也会让推理探针收到 402/429，误判渠道推理面故障。

## What Changes

- 探针取样改为从池内选择**非禁用且不在冷却期**的 key，并按渠道轮换（round-robin），不再固定首键。
- 全部 key 不可用时探针跳过本轮，不产生失败计数、不推进降级；数据面池耗尽语义不变。
- 取样仍为只读：不调用 `Pick/Report`，不写 EMA/冷却/禁用。
- 增加只读取样视图（不改 balance 使用的 `KeyView` 语义），并补首键禁用、首键冷却、全不可用、轮换四类测试。

## Capabilities

### New Capabilities

无

### Modified Capabilities

- `upstream-health`：探针取样必须避开禁用/冷却 key 并轮换；全不可用时跳过而非计失败。

## Impact

- 代码：`internal/health`（Manager 取样与轮换）、`internal/keypool`（只读可用键视图）、装配层快照构造。
- 行为：探针请求可能换用非首键；单键撤销不再永久熔断渠道；无可用 key 的渠道不再因探针失败降级。
- 测试：首键 disabled/冷却、全不可用、轮换、零 key 池写入。
- 依赖：零新增依赖；无存储迁移。
