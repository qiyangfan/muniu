# ADR-0025:keypool 冷却桶独立计时与按桶持久化

- 状态:已接受(2026-11-27;openspec `keypool-cooldown-bucket-isolation` 承载)
- 背景:`keyState` 只有一个 `cooldownUntil` 与一个 `cooldownBucket` 标签,credit 与 rate 共用同一槽位。欠费冷却期间的在途请求收到 429 会把标签改成 rate,之后充值回调只认 credit 桶,key 被剩余欠费冷却卡到自然到期;反向同理,rate 冷却被欠费证据覆盖会让充值释放误清限频冷却。持久化同样只能保存一个桶。
- 关联:ADR-0019(渠道健康)、openspec `keypool-scheduling`、`keypool-cooldown-bucket-isolation`

## 决策

1. **三个独立截止**:`untilCredit`、`untilRate`、`untilTransient(5xx)` 互不覆盖;可用性 = 未禁用且三个截止均不晚于当前。`Pick`、`StateOf("cooldown")`、`CooldownRemain` 都按「任一活跃即不可用/取最大剩余」执行。
2. **事件只写自己的桶**:429 只推进 rate,欠费证据只推进 credit,5xx 连续计数只推进 transient。成功清零全部连续计数,但只清理已到期桶,不取消仍在计时的其他桶。
3. **释放语义按桶精确**:余额回写 `exhausted=false` 只清除 credit 并清零欠费阶梯;rate/transient 不受影响;rate 只按自身到期恢复;transient 由到期或后续成功清零。
4. **按桶持久化**:`key_cooldowns` 唯一键改为 `(provider, key_id, bucket)`;credit 全程落库、rate ≥5 分钟落库、transient 不落库;清除事件只删对应桶行;`ev_seq` 守卫按行生效。
5. **迁移**:SQLite 重建表并复制现有行(bucket 列已存在),存量单行天然对应一个桶;迁移事务内完成,可重入;回滚使用迁移前备份。
6. **观测面不扩**:`StateOf` 字符串仍为 healthy/cooldown/disabled,管理面如需桶详情另立字段。

## 备选与否决

- 保留单槽、用优先级规则决定标签:仍无法同时保留两个桶的阶梯与重启恢复,充值释放语义继续丢信息——否决。
- 每个桶一张表:三份近似 DDL 与查询,维护面翻倍——否决。
- transient 也落库:30 秒瞬态写放大,重启收益仅一次请求——否决。

## 后果

- 新增迁移;`internal/keypool` 状态结构与持久化接口按桶调整。
- 仅修复边缘覆盖场景;常规冷却阶梯、WRR 权重、对外状态字符串不变。
- 测试必须覆盖:在途 429 不覆盖欠费桶、充值只释放 credit、双桶并存、重启恢复双桶、transient 不落库。
- 回滚:恢复旧表结构与单行数据;无外部 API 契约变化。
