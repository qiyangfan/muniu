# Design: keypool-cooldown-bucket-isolation

## Context

`keyState` 当前为 `cooldownUntil + cooldownBucket + consec429/consec5xx/consecCredit`。`enterRateCooldown`/`enterCreditCooldown` 都写同一 `cooldownUntil` 并覆盖标签；持久化 `key_cooldowns` 主键为 `(provider, key_id)`，一 key 一行。见 proposal.md「Why」。

## Goals / Non-Goals

**Goals:**

- 三个冷却来源互不覆盖，释放语义按桶精确。
- 持久化与重启恢复保留每个长冷却桶。
- 现有阶梯数值、Pick 过滤与 WRR 权重不变。

**Non-Goals:**

- 不改变冷却阶梯参数（429 60s→30m、credit 30m→2h、5xx 30s）。
- 不做按模型的 key 冷却。
- 不改变 `StateOf` 对外字符串集合（healthy/cooldown/disabled）。

## Decisions

1. **三个独立截止时刻**：`untilCredit`、`untilRate`、`untilTransient`。`available(now) = !disabled && now >= max(untilCredit, untilRate, untilTransient)`。理由：可用性是「任一桶冷却即不可用」，用 max 判定最直观；释放事件只清自己桶。
2. **连续计数与桶绑定**：`consecCredit` 只随 credit 事件推进，`consec429` 只随 rate，`consec5xx` 只随 transient；成功清零所有连续计数，但只清已到期的桶（与现语义一致）。在途 429 进入 rate 桶不会触摸 credit。
3. **持久化按桶多行**：`key_cooldowns` 新主键 `(provider, key_id, bucket)`；credit 全程、rate ≥5min 落库；transient 不落库。清除事件按 bucket 删行；`ev_seq` 守卫按行生效，迟到的同桶旧事件被忽略。启动 `LoadCooldowns` 逐行恢复对应桶与阶梯。
4. **迁移**：SQLite 不能直接改主键，新增迁移重建表并 `INSERT ... SELECT` 现有行（bucket 列已存在），随后删旧表/改名；迁移必须可重入（IF NOT EXISTS / 事务）。存量单行天然对应一个桶，无信息损失。
5. **对外观测**：`StateOf` 只要任一桶活跃即 `cooldown`；`CooldownRemain` 返回最大剩余；`StateCounts` 不变。管理面如展示桶详情，另立字段，不在本变更扩大。

## Risks / Trade-offs

- [主键迁移失败] → 事务内重建与复制，启动加载失败按现纪律降级为空恢复（最坏多探活一次）。
- [多行写入放大] → 仅 credit 全程与 rate 长冷却落库，频率低；transient 不落库。
- [success 清桶边界] → 只清已到期桶；测试覆盖「成功时另一桶仍活跃」。
- [旧代码读新表] → 同一发布内升级，无跨版本混读；回滚需保留旧表备份，迁移脚本记录。

## Migration Plan

1. 新增迁移：重建 `key_cooldowns` 为 `(provider,key_id,bucket)` 主键。
2. 启动加载兼容旧行（bucket 列已有）。
3. 回滚：恢复旧表结构与单行数据（迁移前备份），无外部契约影响。
