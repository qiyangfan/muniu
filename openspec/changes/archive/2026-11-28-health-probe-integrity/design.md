# Design: health-probe-integrity

## Context

`health.Channel.Keys` 由装配层从 `keypool.Snapshot()` 构造，`Snapshot` 的结果带 `Disabled` 但 health 只取 `ID/Value`；`probeSend` 用 `firstKeyValue` 固定取首个非空值。key 冷却信息不在 `KeyView` 里。

## Goals / Non-Goals

**Goals:**

- 探针只使用当前可服务 key；单键撤销/冷却不污染渠道健康。
- 取样轮换，避免长期只覆盖一把 key。
- 保持探针零状态写入。

**Non-Goals:**

- 不改变探针端点、间隔、鉴权与状态机。
- 不实现按 key 粒度的健康档案。
- 不改变数据面 `Pick` 的 WRR/冷却语义。

## Decisions

1. **新增只读 `keypool.ProbeKeys(now)`**，返回非禁用且不在冷却期的 `{ID, Value}` 切片；不扩 `KeyView`，避免 balance 快照凭空多出冷却字段、改变其语义。备选是给 `KeyView` 加 `CooldownUntil`，会让 balance 消费方也看到无关状态，否决。
2. **轮换状态放在 health.Manager**：每渠道维护一个原子/加锁的下标，每轮取下一个可用 key。key 列表每轮来自 `Sources()`，热替换后自然对齐；下标越界按取模处理。
3. **全部不可用 → 跳过本轮探针**：不调用 `probeSend`，不更新 `LastProbeAt/LastProbeOK/FailStreak`；仅执行 `advanceTick` 推进半开计时。理由：渠道不可服务已由池耗尽路径暴露；把「没有可用 key」当探针失败会让渠道永久降级且无法自愈。
4. **只读纪律**：取样直接读池状态，不调用 `Pick/Report`；探针仍不写 EMA/冷却/禁用。

## Risks / Trade-offs

- [轮换下标与热替换 key 列表变化] → 越界取模，下一轮重新对齐；无持久化需求。
- [全不可用跳过可能延迟渠道降级] → 数据面池耗尽已快速失败并告警；渠道健康不再叠加误导性降级。
- [多 key 轮换增加探针覆盖] → 每轮仍只发一次请求，成本不变。
- [keypool 新只读 API] → 仅 health 消费，测试钉住零写入；无外部契约变化。

## Migration Plan

无迁移。上线后首轮探针即按新取样执行；观察 `provider_degraded` 事件是否减少（单键撤销场景）。
