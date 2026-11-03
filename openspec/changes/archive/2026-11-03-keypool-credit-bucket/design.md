# Design

## 结构决策

**`Outcome` 枚举尾追加,而非 Outcome 加 RestReason 字段**:`Report(k, outcome, now, retryAfter)` 调用点签名零变更;枚举值稳定(持久化值即枚举序,尾追加不扰动既有值)。欠费桶与 429 桶共享冷却机制,差异只在「时长阶梯」与「恢复来源」。

**欠费证据判定在 proxy 上报 lane,quirk 层不动**:`ErrorClass` 是 adapter 契约面(quirks.go 把 DeepSeek 402 归限流类是 2026-09-11 的判定),为欠费桶新增 ErrorClass 要动判定表=契约变更,否决。上报点(metering.go reportOutcome 附近)手上已有响应上下文:HTTP 状态码 + 错误体错误码,补一处白名单判定即可。白名单做成表驱动(状态码 → 错误码集合),后续证据驱动增量,不做文案正则。

**2h 封顶的推导**:API key 欠费=充值即恢复(muniu 2026-09-11 开放项裁决语义),封顶过长反伤恢复速度;7d 阶梯是 magpie 订阅窗口世界(5h/weekly reset)的参数,不抄。封顶只兜「无余额源的渠道」的自愈上界;有余额源的 key 由回写提前释放(充值后下一轮评估即复位,评估周期分钟级)。

## 持久化设计

- 表(迁移 0018):`(provider, key_id)` 主键 + `bucket`(credit|rate_limit)/`cool_until`/`consec`;状态变更时池锁内记脏标记、锁外后台刷写(500ms 批量 UPSERT),失败保留脏标记续传(metering pipeline 纪律);崩溃丢 ≤500ms 增量,最坏=重启后对死 key 多探活一次,可接受。
- 加载:启动装配时一次性 SELECT,只恢复 `cool_until > now` 的行进池内存态;`consec` 一并恢复。
- 清理:SIGHUP 拓扑替换后按现配置 key 集合对账,孤儿行删除;键存在则运行态优先、行作恢复源。
- 429 桶落库条件:冷却时长 ≥5min(即 Retry-After 指数推进到第 3 步之后才落库;60s/2min 瞬态不写)。

## 余额回写接线

balance `Service` 评估循环已逐 key 产出(`observe` → `[]KeyBalance`),新增可选回调字段 `OnKeyEvaluated(provider, keyID string, exhausted bool, now time.Time)`(nil=零开销,维持独立运行语义);仅逐 key 源回调,账户级源不回调。装配层把回调接到 `pool.RecordCreditExhausted(keyID, exhausted, now)`:

- `exhausted=true`:幂等——已在欠费冷却中则**不推进阶梯不刷新至时刻**(重复评估无害);否则记 30min 冷却。
- `exhausted=false`:无条件解除欠费冷却 + 清零阶梯(充值即恢复;「>0 就释放」避免误伤——余额源查询失败时不回调,维持状态)。
- 探针、健康熔断、语义缓存零触碰(各自边界维持)。

## 边界

- 与 `upstream-health` 的分工:探活归健康子系统(其「探针零触碰池」边界不动);本项只管池内记忆 + 余额回写。
- 单候选渠道欠费 = 该渠道整 key 不可用:快速失败语义由 upstream-health 既有候选域收缩承接,本项不重复造。
- 阶梯推进计数与 EMA 相互独立(欠费冷却只挡 Pick,不改变 EMA 语义)。

## 测试策略

- 状态机表驱动失败测试先行:402/insufficient_quota/文案不触发/阶梯推进封顶/成功清零(先红后绿)。
- 持久化:落库阈值(≥5min 写、30s 不写)、重启恢复、SIGHUP 对账清孤儿、脏刷写失败续传。
- 回写:幂等(重复评估不推进)、充值释放、账户级不回调、nil 回调零开销。
- 突变验证:破坏白名单(让文案触发)与破坏封顶(阶梯无限增长)各一处,测试必须变红。
