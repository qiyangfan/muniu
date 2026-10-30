# provider-balance-alerting Specification

## Purpose
渠道上游余额的运营观测与主动告警:按渠道从多源查询真实余额(官方 Bearer 接口、云账务签名接口、手动录入),结合网关自身计量外推剩余可用时间,在余额低于绝对阈值或剩余时间穿越档位时经 webhook 主动通知——把上游欠费导致的业务中断提前为预算式运维。

## Requirements

### Requirement: 余额源按渠道声明且缺省关闭

每个渠道必须 (MUST) 通过 `providers[].balance.source` 显式声明余额源,值域固定为 `none`(缺省)/`deepseek`/`moonshot`/`openrouter`/`billing-compat`/`aliyun-bss`/`volc-bss`/`manual`;未声明或声明为 `none` 的渠道必须 (MUST) 不产生任何余额观测行为(零请求、零落库、零告警),数据面行为与本能力引入前逐字节一致;非法 kind 必须 (MUST) 在配置校验期 fail-fast,不得等到运行期才暴露。

#### Scenario: 缺省渠道零行为

- **WHEN** 渠道未声明 `balance` 字段或声明 `source: none`,且 `balance.enabled: true`
- **THEN** 该渠道不发起任何余额查询,不产生余额落库与告警,请求转发路径与本能力引入前一致

#### Scenario: 非法 kind 启动期拒绝

- **WHEN** 配置声明 `balance.source: gemini`(值域外)
- **THEN** 进程启动失败,校验错误指明渠道名与合法值域

### Requirement: Bearer 直查余额源的解析语义

`deepseek`/`moonshot` 两种 kind 必须 (MUST) 使用渠道 key 池内的 key 以 Bearer 方式请求上游官方余额端点(base_url 同源固定路径),并按各家官方响应契约解析:`deepseek` 取 `balance_infos` 数组的 `total_balance`(十进制字符串)与 `currency`(CNY/USD);`moonshot` 取 `data.available_balance`(人民币元数值,含代金券)。响应字段缺失、类型不符或 HTTP 非 2xx 必须 (MUST) 判定为该 key 的查询失败,不得产生部分余额。

#### Scenario: deepseek 多币种余额解析

- **WHEN** 上游返回 `balance_infos: [{currency: "CNY", total_balance: "110.00"}]`
- **THEN** 该 key 记录余额 110.00、币种 CNY、来源 deepseek、观测时刻为响应收到时刻

#### Scenario: moonshot 数值型响应解析

- **WHEN** 上游返回 `{"code": 0, "data": {"available_balance": 49.58894, ...}}`
- **THEN** 以十进制整数刻度记录 49.58894 元,浮点表示误差不得进入存储值

#### Scenario: 上游故障不产生部分余额

- **WHEN** 余额端点返回 HTTP 500 或响应缺少目标字段
- **THEN** 该 key 本次查询判定失败,既有余额快照保持不变,不写入任何新快照

### Requirement: openrouter 不限额键的诚实降级

`openrouter` kind 必须 (MUST) 请求 `/api/v1/key` 并以 `data.limit − data.usage` 计算余额;当 `data.limit` 为 null(不限额键)时必须 (MUST) 明确标记该 key「余额不可知」——不产生余额、不发余额类告警,但日耗速率仍须 (MUST) 正常统计与展示。

#### Scenario: 不限额键不误报

- **WHEN** 上游返回 `data.limit: null` 且 `data.usage: 12.5`
- **THEN** 该 key 余额标记不可知,不发任何余额告警,日耗速率照常展示 12.5 USD

#### Scenario: 限额键余额可算

- **WHEN** 上游返回 `data.limit: 100` 且 `data.usage: 37.2`
- **THEN** 该 key 记录余额 62.8 USD

### Requirement: billing-compat 聚合中转的余额语义

`billing-compat` kind 必须 (MUST) 请求 base_url 同源的 `/v1/dashboard/billing/subscription` 与 `/v1/dashboard/billing/usage`(Bearer),余额 = `hard_limit_usd − total_usage/100`(total_usage 为声明币种的百分之一);任一端点失败或字段缺失必须 (MUST) 整体判失败。该字段族不自带可信币种语义——聚合中转实现随站点显示配置可产出 CNY 等数值,协议面不可自动判定——因此币种缺省记 USD,且必须 (MUST) 允许经 `balance.currency` 显式声明(CNY|USD);其余 Bearer 源的上游自述币种,声明 `currency` 必须 (MUST) 在配置校验期被拒绝。

#### Scenario: one-api 家族端点求差

- **WHEN** subscription 返回 `hard_limit_usd: 50.0` 且 usage 返回 `total_usage: 1250`
- **THEN** 该 key 记录余额 37.5 USD、币种 USD

#### Scenario: CNY 显示中转站点显式声明币种

- **WHEN** 渠道声明 `source: billing-compat` 且 `currency: CNY`(该中转站点按人民币显示额度),subscription 返回 `hard_limit_usd: 360.0` 且 usage 返回 `total_usage: 0`
- **THEN** 该 key 记录余额 360、币种 CNY,below 阈值与燃速衰减均按 CNY 口径参与估算,不因数值字段名含 usd 而误标 USD

### Requirement: 云账务 AK/SK 余额源的签名查询

`aliyun-bss` 与 `volc-bss` kind 必须 (MUST) 使用配置引用的云厂商 AccessKey ID/Secret(经 `${VAR}` 环境变量注入)对账务端点完成各自官方签名(阿里云 RPC HMAC 签名调用 `QueryAccountBalance`;火山引擎 Signature V4 调用账务余额查询),解析账户可用余额与币种;凭证缺失、签名被拒或权限不足必须 (MUST) 判定为查询失败并进入失败容忍路径,凭证值必须 (MUST) NOT 出现在任何日志与告警载荷中。

#### Scenario: 阿里云账务余额解析

- **WHEN** `aliyun-bss` 签名请求成功且响应含可用余额字段
- **THEN** 渠道记录余额与币种(CNY),来源标记 aliyun-bss

#### Scenario: 凭证错误显式失败

- **WHEN** 云账务端点以签名/权限类错误拒绝(如 AccessKey 无效、未授权账务只读权限)
- **THEN** 本次查询判失败并计入连续失败计数,错误归类为凭证/权限问题而非网络故障,凭证值不出现在日志中

### Requirement: 手动录入余额快照

`manual` kind 必须 (MUST) 仅通过管理 API 录入余额快照(金额十进制字符串 + 币种声明),录入即落库并立即参与有效余额合成与告警评估;金额格式非法、非正数或币种缺省必须 (MUST) 以 400 拒绝;无任何上游外呼。

#### Scenario: 录入即生效

- **WHEN** 向 manual 渠道录入 `{"amount": "12.34", "currency": "CNY"}`
- **THEN** 该渠道立即可见余额 12.34 CNY 并参与下一次评估,无需重启

#### Scenario: 非法金额拒绝

- **WHEN** 录入 `{"amount": "abc"}` 或 `{"amount": "-1"}`
- **THEN** 返回 400 与字段级错误,既有余额不变

### Requirement: 多 key 渠道的余额归属与保守估算

余额是账户级数据;池内每把 key 必须 (MUST) 独立查询、独立存储余额与币种;渠道的剩余时间估算必须 (MUST) 使用池内最小有效余额(哪个账户先耗尽先告警);单把 key 查询失败必须 (MUST) NOT 影响其余 key 的观测,全部 key 失败才计入渠道级失败。

#### Scenario: 双 key 取最小保守估算

- **WHEN** 渠道池内 key A 余额 80 CNY、key B 余额 30 CNY,渠道日耗 10 CNY
- **THEN** 渠道剩余估算基于 30 CNY(≈3 天),而非 110 CNY 均摊

#### Scenario: 单 key 失败不牵连

- **WHEN** 池内 key A 查询成功、key B 超时
- **THEN** key A 快照正常更新,渠道观测不判整体失败

### Requirement: 有效余额合成与币种纪律

有效余额必须 (MUST) 等于最近快照余额减去快照时刻之后该渠道在 `usage_logs` 中的计量消耗(实时衰减,无须等下次上游查询);币种以其快照声明的币种为准;当快照币种 ≠ 结算币种时,剩余时间估算必须 (MUST) 禁用并在查询与管理面明示「币种不一致,无法估算天数」,绝对阈值告警不受影响;必须 (MUST) NOT 跨币种直接相减或相除。

#### Scenario: 快照后消耗实时衰减

- **WHEN** deepseek 快照 100 CNY 后,该渠道经网关消耗 3.5 CNY 计量落账
- **THEN** 查询接口返回有效余额 96.5 CNY,期间未发起上游余额查询

#### Scenario: 币种不一致禁估算

- **WHEN** 结算币种为 CNY,某 openrouter key 余额为 USD
- **THEN** 余额与绝对阈值告警正常,剩余天数估算禁用并展示禁用原因

### Requirement: 消耗速率与剩余时间估算

剩余时间估算必须 (MUST) 基于配置回看窗口(缺省 7 天)内该渠道在 `usage_logs` 的消耗推导日均速率,剩余时间 = 最小有效余额 ÷ 日均速率;窗口内零消耗必须 (MUST) 判定「无消耗,剩余不限」且不发任何档位告警;可用样本不足(窗口内消耗天数低于配置最小样本天数,缺省 2 天)必须 (MUST) 判定「数据不足,无法估算」并明示;估算只对币种与结算币种一致的余额进行。

#### Scenario: 正常外推落档

- **WHEN** 有效余额 45 CNY,回看窗口日均消耗 9 CNY
- **THEN** 剩余估算 ≈5 天,与档位表比对进入告警判定

#### Scenario: 零消耗不发档位告警

- **WHEN** 渠道余额 5 CNY 但回看窗口内计量消耗为零
- **THEN** 判定无消耗、剩余不限,不发档位告警(绝对阈值告警不受此约束)

#### Scenario: 数据不足诚实降级

- **WHEN** 渠道启用仅 1 天,窗口内仅 1 天有消耗
- **THEN** 不产生剩余估算,管理面展示「数据不足」而非臆测值

### Requirement: 绝对阈值与档位的告警状态机

每 (渠道, 规则) 维护持久化告警状态:余额低于配置绝对阈值(余额币种)时触发 `balance_below` 事件;剩余时间自高向低穿越档位表(缺省 30 天/14 天/7 天/3 天/1 天,配置可调)时逐档触发 `balance_tier` 事件,严重级随档位深入而升高(1 天档为 critical);同一规则处于已触发态时必须 (MUST) NOT 重复发射,估算回升超过触发线 × 迟滞系数(缺省 1.2)才解除并重新武装;进程重启后必须 (MUST) 从持久状态恢复,不重复告警已触发规则。

#### Scenario: 穿越档位单次告警

- **WHEN** 剩余估算从 8 天降至 2.4 天,档位表含 3 天档
- **THEN** 发射一次 3 天档告警;后续评估估算仍为 2 天时不重复发射

#### Scenario: 迟滞解除后重新武装

- **WHEN** 3 天档已触发,充值后估算回升至 4 天(超过 3 × 1.2)
- **THEN** 该档位解除;随后估算再跌破 3 天时重新发射

#### Scenario: 重启不重复告警

- **WHEN** 1 天档处于已触发态时进程重启,重启后首次评估估算仍低于 1 天
- **THEN** 不重复发射 1 天档告警

#### Scenario: 绝对阈值独立触发

- **WHEN** 渠道余额 8 CNY 低于配置阈值 10 CNY,且渠道无消耗数据(无天数估算)
- **THEN** 触发 `balance_below` 告警,severity 为 critical

### Requirement: 余额源失败容忍与运维告警

单次余额查询失败必须 (MUST) 只记日志与状态,保持既有快照;同一渠道连续失败达到配置阈值(缺省 3 次)必须 (MUST) 经既有告警管道发射一次 `balance_source_error` 事件(warning),此后不重复发射直至出现一次成功(成功即重置计数并解除);余额观测全链必须 (MUST) 异步于数据面,查询超时(缺省 10 秒)不得阻塞、拖慢或失败化任何转发请求。

#### Scenario: 连续失败单次通知

- **WHEN** 某渠道余额端点连续 3 次超时
- **THEN** 发射一次 `balance_source_error`;第 4、5 次继续失败不重复发射

#### Scenario: 成功重置

- **WHEN** 连续失败已触发运维告警后,下一次查询成功
- **THEN** 失败计数清零,告警态解除,后续再连续失败时重新计数

#### Scenario: 数据面零感知

- **WHEN** 余额评估循环正在对某渠道上游发起查询且上游挂起
- **THEN** 数据面请求转发不受任何影响

### Requirement: 定时评估循环

余额评估必须 (MUST) 由后台循环驱动:进程启动即评估一次,此后按配置间隔(缺省 1 小时)评估,渠道间以固定比例错峰(防同刻并发外呼);循环必须 (MUST) 可优雅停止(进程关闭时在途查询终止、状态落库),挂接进程 cleanup;评估所需的全部状态(快照、告警态)必须 (MUST) 持久于存储,重启后无缝恢复。

#### Scenario: 启动即评估

- **WHEN** 进程启动且存在已声明余额源的渠道
- **THEN** 首轮评估立即执行,不等首个间隔期满

#### Scenario: 优雅停止

- **WHEN** 进程收到关闭信号时有评估在途
- **THEN** 在途查询终止,已取得的快照与状态变更落库后进程退出

### Requirement: 管理 API 余额查询与操作

管理面必须 (MUST) 提供三个端点并纳入既有管理面鉴权与审计:`GET /admin/v1/balances` 返回全部已声明渠道的观测总览(每 key 余额与币种、有效余额、回看窗口日耗、剩余估算或禁用原因、各规则告警态、最近检查时刻与最近错误);`POST /admin/v1/balances/{provider}/manual` 录入手动快照;`POST /admin/v1/balances/{provider}/refresh` 对该渠道立即触发一次余额查询(异步执行,查询结果于总览可见);管理 API 契约必须 (MUST) 同步进 OpenAPI 单源并再生前端类型。

#### Scenario: 总览包含告警态与估算

- **WHEN** 某 deepseek 渠道已观测且 3 天档已触发
- **THEN** 总览返回该渠道每 key 余额、有效余额、日耗、剩余估算、`balance_tier` 触发态与最近检查时刻

#### Scenario: 立即刷新

- **WHEN** 对某渠道调用 refresh
- **THEN** 该渠道余额查询立即发起,总览的最近检查时刻随之更新

### Requirement: 配置契约

`balance` 顶层节必须 (MUST) 提供:总开关(缺省开启)、评估间隔秒(缺省 3600)、查询超时秒(缺省 10)、回看窗口天(缺省 7)、最小样本天(缺省 2)、迟滞系数(缺省 1.2)、连续失败阈值(缺省 3)、档位表(缺省 [30d,14d,7d,3d,1d],以小时配置);`providers[].balance` 必须 (MUST) 提供 source、绝对阈值 `below`(余额币种十进制字符串,可选)、云凭证引用(aliyun-bss/volc-bss 必填 AK/SK 环境变量名,gcp 类凭证不在值域);币种声明约束:manual 必填(CNY|USD)、billing-compat 可选(缺省 USD)、其余源声明必须 (MUST) 在配置校验期拒绝;全部校验必须 (MUST) 攒齐一次报全并保证 example 配置样例同步,缺失凭证引用的签名类来源必须 (MUST) 校验期拒绝。

#### Scenario: 签名源缺凭证引用拒绝

- **WHEN** 渠道声明 `source: aliyun-bss` 但未配置 AK/SK 环境变量引用
- **THEN** 启动校验失败,错误指明缺失字段

#### Scenario: 非法档位表拒绝

- **WHEN** 档位表配置含 0 或负值小时数
- **THEN** 启动校验失败并列出非法项,不部分生效

#### Scenario: 校验错误一次报全

- **WHEN** 两个渠道分别存在非法 kind 与非法阈值
- **THEN** 校验错误同时包含两处问题

### Requirement: 异常消耗告警旋钮

异常消耗告警旋钮必须 (MUST) 以指针三态承接:`burn_spike_ratio_milli`(燃速突变倍数,毫单位)缺省 5000、`drop_ratio_milli`(余额骤降比例,毫单位)缺省 500;未配置取缺省,显式 0 是合法禁用形态,负值必须 (MUST) 在配置校验期拒绝并指明「非负毫单位,0=禁用」。

#### Scenario: 异常告警旋钮负值拒绝

- **WHEN** 配置声明 `burn_spike_ratio_milli: -1` 或 `drop_ratio_milli: -500`
- **THEN** 启动校验失败,错误指明字段与「非负毫单位,0=禁用」的合法形态

### Requirement: 燃速突变告警

渠道级燃速突变告警必须 (MUST) 以「当前回看窗口日均消耗 ÷ 紧邻上一等长窗口日均消耗」为判据:比值达到配置倍数(`burn_spike_ratio_milli`,毫单位,缺省 5000 即 5 倍,0=禁用)时经既有告警管道发射一次 `balance_burn_spike` 事件(warning),载荷携带两窗口日均消耗与比值;两窗口各自的有消耗天数均必须 (MUST) 达到配置最小样本天数,上一窗口零消耗或任一窗口样本不足必须 (MUST) 不判定(诚实无基线,不臆测);比值回落到倍数 ÷ 迟滞系数以下必须 (MUST) 解除并重新武装,处于已触发态时必须 (MUST) NOT 重复发射;进程重启后必须 (MUST) 从持久状态恢复,不重复告警;该告警只挂接在已声明余额源的渠道,未声明或 `none` 渠道必须 (MUST) 保持零行为。

#### Scenario: 突变触发单次发射

- **WHEN** 渠道当前窗口日均消耗 10 CNY、上一等长窗口日均 1.5 CNY(比值 ≈6.7 ≥ 缺省 5 倍)
- **THEN** 发射一次 `balance_burn_spike` 事件(warning),载荷含两窗口日均与比值;后续评估比值仍 ≥ 5 时不重复发射

#### Scenario: 基线不足不判定

- **WHEN** 上一窗口仅 1 天有消耗(低于最小样本天数 2)或上一窗口零消耗
- **THEN** 不判定、不发射任何燃速告警,状态保持原样

#### Scenario: 回落解除并重新武装

- **WHEN** 燃速告警已触发,窗口滚动后比值回落到 4(低于 5 ÷ 1.2 ≈ 4.17)
- **THEN** 告警解除并重新武装;之后再次达到 5 倍时重新发射

### Requirement: 余额骤降告警(逐 key)

Bearer 查询源(`deepseek`/`moonshot`/`openrouter`/`billing-compat`)的每把 key,相邻两次成功快照之间余额跌幅比例达到配置阈值(`drop_ratio_milli`,毫单位,缺省 500 即 50%,0=禁用)时必须 (MUST) 经既有告警管道发射一次 `balance_cliff` 事件(warning),载荷携带 key 标识、前后余额与跌幅比例;`manual` 与云账务源必须 (MUST) NOT 参与该判定(人工录入天然跳变、云账务有月账单周期性大跌);首次成功快照无前值、前后币种不一致、余额上升(充值/限额上调)时必须 (MUST) 不判定;一次骤降必须 (MUST) 只发一次,其后必须 (MUST) 出现一次跌幅低于阈值的平稳观测才重新武装;进程重启后必须 (MUST) 从持久状态恢复;查询失败不产生新快照,不构成骤降判定点。

#### Scenario: 骤降触发

- **WHEN** 某 deepseek key 相邻两次成功快照余额从 100 CNY 变为 40 CNY(跌 60% ≥ 50%)
- **THEN** 发射一次 `balance_cliff` 事件(warning),载荷含 key 标识、前后余额与跌幅比例

#### Scenario: 自然消耗不误报

- **WHEN** 相邻两次成功快照间余额跌幅 3%(窗口内稳定消耗的自然衰减)
- **THEN** 不发射骤降告警

#### Scenario: 平稳观测后重新武装

- **WHEN** 骤降告警已触发,随后一次成功快照跌幅低于 50%(平稳观测),之后该 key 再次骤跌 60%
- **THEN** 第一次骤跌后不重复发射;平稳观测重新武装后,再次骤跌重新发射

#### Scenario: 币种变化不判定

- **WHEN** 某 key 前值快照币种 CNY、新快照币种 USD(渠道配置变更)
- **THEN** 本次不判定、不发射,状态保持原样
