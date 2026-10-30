# Design

## Context

既有评估链:`Service.evaluateProvider` 每渠道查询落库 → `EstimateChannel`(当前窗口燃速 → 剩余小时)→ `Evaluate`(below/tier 状态机)→ `Notifier.EmitContext`。燃速数据来自 `store.BurnWindow`(usage_logs 逐日聚合,仅当前窗口);`balance_alert_states` 以 (provider, rule_key) 持久化触发态;`provider_balances` 每 key 一行,成功查询 upsert 覆盖、失败保持。见 proposal.md「Why」。

## Goals / Non-Goals

**Goals:**

- 两条新规则挂接既有评估循环与状态机,事件经既有 alerting 管道,管理面零新端点。
- 判定逻辑全部落在纯函数层(evaluator),测试先行覆盖负面用例(样本不足/币种不一致/禁用/重启恢复)。

**Non-Goals:**

- 不做告警事件回放队列(webhook 宕机期间的事件不补发,既有语义不变)。
- 不做 per-channel 旋钮(两旋钮为 balance 顶层全局,与迟滞/档位同级)。
- 不给燃速突变设绝对消耗下限(小基线噪声为已接受权衡,见 Risks)。
- 不改 below/tier/source_error 既有语义;不给 `source: none` 渠道引入任何新行为。

## Decisions

**D1 骤降判据载体 = `provider_balances` 平移列,不是内存比较,也不是历史表。**
成功写入时旧行余额/币种/时刻平移进新增 prev 三列(`prev_balance_credits`/`prev_currency`/`prev_as_of_at`,迁移只追加),骤降判定在评估层读行完成(prev vs current)。备选:(a) queryPhase 写前内存比较——判定逻辑落 IO 层且与写路径耦合,不可纯函数测试;(b) 追加式快照历史表——无限增长需配修剪,而判定只需要最后一次相邻转移。平移列天然满足:失败查询不转移(前值不动)、重启后判据与状态同在库里、每转移只评估一次且幂等。

**D2 规则键:`burn_spike`(渠道级)与 `cliff:<key_id>`(逐 key)。**
复用 (provider, rule_key) 状态面与 `EventKey` 事件键形态。逐 key 是因为骤降是账户级事件、重武装语义按 key 独立;渠道级聚合会丢失「哪把 key 被抽干」。

**D3 燃速突变判定用整数毫单位比值。**
`ratioMilli = curBurnPerDay × 1000 ÷ prevBurnPerDay`(prev > 0 才判定);触发线 = `burn_spike_ratio_milli`(缺省 5000);解除线 = 触发线 ÷ 迟滞系数(复用 `scaledLine` 取倒数形态)。上一等长窗口聚合新增 store 区间查询(现有 BurnWindow 仅 fromUTC);两窗口各有消耗天数均 ≥ `burn_min_days` 才判定。severity 固定 warning——持续异常最终会把剩余时间压进档位由 tier 告警升格 critical,两级信号各司其职。

**D4 骤降只判跌、只对 Bearer 源。**
上升(充值/限额上调)不是告警对象;manual(人工录入跳变)与云账务(月账单周期大跌)排除。币种不一致或缺前值不判定(状态保持,与既有「判据不可得不动状态」纪律一致)。平稳重武装 = 出现一次跌幅低于阈值的成功转移;查询失败不产生新转移。

**D5 管理面零新端点。**
总览 alerts 数组按 rule_key 泛型透出,新规则键自动可见;前端仅补规则键→中文文案映射。OpenAPI 结构不变。

## Risks / Trade-offs

- [小基线噪声:上一窗口消耗极小时,微小绝对增量即可达 5 倍] → v1 接受(阈值可调、0 可禁用);below/tier 告警对真金白银的耗尽仍有独立兜底;若运营期误报显著,再补绝对下限旋钮(记 backlog 不进本 change)。
- [openrouter 站点侧调低 hard_limit 会表现为余额骤降,非网关侧消耗] → warning 级 + 载荷携带前后余额可人工研判;误报成本为一条通知,可接受。
- [cliff 状态随 key 数增长(cliff:<key_id> 每把 key 一行)] → 状态行轻量且 `PruneBalanceKeys` 已有 key 拓扑清理先例,评估循环同步清理失效 key 的规则行。
- [迁移加列后回滚] → 新列对旧代码无感知,前向兼容;不做降级路径(单二进制本地库,回滚即回退二进制)。

## Migration Plan

`ALTER TABLE provider_balances ADD COLUMN` 三列(只追加纪律),随启动迁移执行;新旋钮缺省即启用(5 倍/50%),升级后首轮评估即生效,配置置 0 可关闭。

## Open Questions

(无)
