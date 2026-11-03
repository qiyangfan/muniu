# Proposal

## Why

欠费 key 的处置存在四个实证缺口(核验证据见 HANDOFF 验收记录 56):

1. **签名覆盖单薄**:欠费识别只有 DeepSeek 一家的 402 quirk(`internal/adapter/quirks.go`,归限流类);OpenAI 兼容族通用的 `insufficient_quota` 错误码等欠费形态落通用 429 桶。
2. **欠费被 429 桶语义拖累**:429 桶冷却 `max429=30min` 封顶——永久欠费的 key 以 ≤30 分钟周期被反复选中探活,每次探活都是一次真实失败尝试(占候选轮转、计失败、污染 EMA)。
3. **冷却纯内存**:重启后对已知死 key 全量重试,冷启动 429/402 风暴;SIGHUP 拓扑热替换虽保留池状态,但进程重启不保留。
4. **观测与调度断裂**:七源余额子系统是纯仪表盘(balance 只读 `keypool.Snapshot`,零回写)——「知道没钱」与「不再往那儿发」之间没有桥。

竞品对照(magpie,2026-11-03 考察):余额耗尽 key 让位、充值后凭余额读数提前复位、欠费阶梯远长于限流退避——思想可移植,参数按 API-key 世界重定(订阅世界的 7d 阶梯不抄)。

## What Changes

- **keypool 新增欠费桶**:`Outcome` 追加 `OutcomeCreditExhausted`(枚举尾追加,`Report` 调用点签名零变更);欠费冷却阶梯 30 分钟起步、连续欠费 ×2 推进、**2 小时封顶**;一次成功即清零阶梯。failover `ErrorClass` 与判定表**零改动**(欠费对换道语义仍归限流类,首 token 前照常换道)。
- **欠费证据判据(封闭白名单)**:上报 lane 依据响应证据分桶——上游 HTTP **402**,或错误体错误码属封闭白名单(如 OpenAI 族 `insufficient_quota`);**不按错误文案模糊匹配**(厂商方言陷阱,竞品自己的维护包袱)。白名单外一切失败维持既有分类。
- **冷却持久化**:迁移 0018 新表(渠道/键/桶类型/至时刻/连续步);冷却时长 ≥5 分钟的状态落库(5xx 30s 瞬态不落库防写放大),启动加载恢复,SIGHUP 拓扑热替换无损(键仍在则恢复,键已删则清行);重启后已冷却 key 不得立即重试。
- **余额回写联动**:装配层消费 balance 评估结果——逐 key 余额源(deepseek/moonshot/openrouter/billing-compat)某 key 有效余额=0 → 记欠费冷却;后续评估余额>0 → **提前解除**冷却并清零阶梯(充值即恢复);账户级源(aliyun-bss/volc-bss/manual)不回写(粒度错配,不得自动降级整渠道);回写幂等(重复评估不推进阶梯)。
- metrics:欠费事件计数 `{cause=upstream|balance}` 与提前释放计数。

## Capabilities

### New Capabilities

- `keypool-scheduling`:欠费分桶、欠费证据白名单、冷却持久化与热替换无损、余额回写联动。

### Modified Capabilities

(无——既有 `upstream-health` 的「探针零触碰池」与 `provider-balance-alerting` 的观测边界不受影响;回写在装配层消费 balance 输出,balance 子系统自身零行为变化)

## Impact

- `internal/keypool`:`Outcome` 常量、欠费桶状态机、持久化读写接口(store 注入)、余额回写入口(幂等)。
- `internal/proxy`(上报 lane):欠费证据采集(状态码 + 错误体错误码,报告点上下文已在手)。
- `internal/store`:迁移 0018 + 冷却表 UPSERT/加载。
- `internal/balance`:评估循环暴露逐 key 结果回调(现仅 `observe` 内部产出;新增可选回调字段,零行为变化)。
- `cmd/muniu`(装配):balance→keypool 回写接线。
- `docs/design/proxy-and-streaming.md` 池语义节补记;`docs/adr/`(如与上游健康熔断边界需要澄清则随文登记,不新立 ADR——本项对外路由行为不变,仅探活频率下降)。
- 零新增依赖。
