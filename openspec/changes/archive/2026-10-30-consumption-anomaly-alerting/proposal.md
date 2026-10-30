# Proposal

## Why

既有余额告警只有三类判据:绝对阈值(below)、剩余时间档位(tier)、余额源失败(source_error)。燃速在其中只是档位告警的估算输入——当渠道余额基数大、异常消耗不足以把剩余时间压进最长档位(缺省 30 天)时,盗刷/死循环调用/上游计费异常这类「钱在异常烧」的情形会静默数天无人知晓。需要两条面向「消耗异常本身」的告警:燃速突变(持续异常)与单次余额骤降(突发悬崖),把发现时机从「余额快耗尽」提前到「消耗行为异常」。

## What Changes

- 新增**燃速突变告警**(`burn_spike` 规则):当前回看窗口日均消耗与上一等长窗口日均消耗之比达到配置倍数(缺省 5 倍)时触发 warning;两窗口均须满足最小样本天数,基线窗口零消耗不判定;走既有迟滞状态机(比值回落到倍数 ÷ 迟滞系数解除)。
- 新增**余额骤降告警**(`cliff:<key_id>` 规则,逐 key):同 key 相邻两次成功快照间余额跌幅达到配置比例(缺省 50%)且币种一致时触发 warning;仅限 Bearer 查询源(manual 为人工录入天然跳变、云账务有月账单周期性大跌,均不适用);一次骤降只发一次,需一次跌幅低于阈值的平稳观测才重新武装。
- 存储面:`provider_balances` 增加「上一次快照余额/币种/时刻」列(写入时平移),骤降判定以此为判据——迁移只追加。
- 配置面:`balance` 顶层节新增 `burn_spike_ratio_milli`(缺省 5000,0=禁用)与 `drop_ratio_milli`(缺省 500,0=禁用),校验攒齐报全,example 样例同步。
- 两类告警经既有 alerting 管道外发,管理面总览经既有 alerts 数组透出(rule_key 泛型,无新端点)。

## Capabilities

### New Capabilities

(无——异常消耗告警是 `provider-balance-alerting` 能力边界的自然延伸,规则/状态机/发射管道/管理面全部复用既有结构)

### Modified Capabilities

- `provider-balance-alerting`:告警状态机 requirement 扩展两类规则(`burn_spike`/`cliff:<key_id>`)的触发、解除与重启恢复语义;配置契约 requirement 扩展两个新旋钮及校验约束;「余额源按渠道声明且缺省关闭」的零行为契约不变——两类新告警只挂接在已声明余额源的渠道上。

## Impact

- `internal/balance`:evaluator 增两条规则判定与解除(纯函数,测试先行域);service 装配新判据输入;estimate/查询路径不变。
- `internal/store`:`provider_balances` 迁移加列(前值三列,只追加);BurnWindow 旁增上一窗口区间聚合查询。
- `internal/config`:两个新旋钮校验;`muniu.example.yaml` 同步。
- 管理面:总览 alerts 数组新增规则键,前端 Balances 页规则文案映射(无新端点、OpenAPI 无结构变化,无需再生类型)。
- `docs/design/balance-alerting.md`:核心算法与纪律、事件与通知两节同步。
