# Proposal: keypool-cooldown-bucket-isolation（冷却桶独立计时）

> 优先级：P2（双桶共享单槽导致充值释放失效）。关联 ADR：`docs/adr/0025-keypool-cooldown-bucket-isolation.md`。

## Why

`keyState` 只有一个 `cooldownUntil` 和一个 `cooldownBucket` 标签，credit 与 rate 两个桶共用同一槽位。欠费冷却期间若有一笔在途请求收到 429，`enterRateCooldown` 会把桶标签改成 rate；之后充值回调只认 credit 桶，key 会被剩余欠费冷却卡到自然到期。反向同理：rate 冷却中被欠费证据覆盖会让充值释放误清限频冷却。重启恢复也只能恢复一个桶。

## What Changes

- key 状态改为 credit / rate / transient(5xx) 三个独立计时器；可用性 = 三者均未到期且未禁用。
- 各事件只更新自己的桶，不再互相覆盖；成功只清连续计数与已过期桶，不取消其他仍在计时的桶。
- 余额回写只释放 credit 桶；rate/transient 不受影响。
- 持久化行改为 `(provider, key_id, bucket)` 唯一；credit/rate 长冷却各自恢复；transient 仍不落库；清除事件只删对应桶行。
- 迁移现有 `key_cooldowns` 表（主键当前为 provider+key_id）到按桶唯一；补双桶并发、充值释放、重启恢复测试。

## Capabilities

### New Capabilities

无

### Modified Capabilities

- `keypool-scheduling`：冷却桶独立计时、桶级持久化与释放语义。

## Impact

- 代码：`internal/keypool`（状态结构、Pick/StateOf/CooldownRemain、persist/Load）、`internal/store`（迁移与行断言）。
- 数据：`key_cooldowns` 主键变更与存量行迁移；无外部 API 契约变化。
- 行为：仅修复边缘覆盖场景；常规冷却阶梯与 WRR 不变。
- 测试：credit+rate 并存、在途 429 不覆盖 credit、充值只释放 credit、重启恢复双桶、5xx 瞬态不落库。
- 依赖：零新增依赖。
