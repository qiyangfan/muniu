# Spec Delta

## ADDED Requirements

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

## MODIFIED Requirements

### Requirement: 配置契约

`balance` 顶层节必须 (MUST) 提供:总开关(缺省开启)、评估间隔秒(缺省 3600)、查询超时秒(缺省 10)、回看窗口天(缺省 7)、最小样本天(缺省 2)、迟滞系数(缺省 1.2)、连续失败阈值(缺省 3)、档位表(缺省 [30d,14d,7d,3d,1d],以小时配置)、燃速突变倍数 `burn_spike_ratio_milli`(毫单位,缺省 5000,0=禁用)、余额骤降比例 `drop_ratio_milli`(毫单位,缺省 500,0=禁用);`providers[].balance` 必须 (MUST) 提供 source、绝对阈值 `below`(余额币种十进制字符串,可选)、云凭证引用(aliyun-bss/volc-bss 必填 AK/SK 环境变量名,gcp 类凭证不在值域);币种声明约束:manual 必填(CNY|USD)、billing-compat 可选(缺省 USD)、其余源声明必须 (MUST) 在配置校验期拒绝;两个异常告警旋钮必须 (MUST) 为非负毫单位,负值校验期拒绝;全部校验必须 (MUST) 攒齐一次报全并保证 example 配置样例同步,缺失凭证引用的签名类来源必须 (MUST) 校验期拒绝。

#### Scenario: 签名源缺凭证引用拒绝

- **WHEN** 渠道声明 `source: aliyun-bss` 但未配置 AK/SK 环境变量引用
- **THEN** 启动校验失败,错误指明缺失字段

#### Scenario: 非法档位表拒绝

- **WHEN** 档位表配置含 0 或负值小时数
- **THEN** 启动校验失败并列出非法项,不部分生效

#### Scenario: 校验错误一次报全

- **WHEN** 两个渠道分别存在非法 kind 与非法阈值
- **THEN** 校验错误同时包含两处问题

#### Scenario: 异常告警旋钮负值拒绝

- **WHEN** 配置声明 `burn_spike_ratio_milli: -1` 或 `drop_ratio_milli: -500`
- **THEN** 启动校验失败,错误指明字段与「非负毫单位,0=禁用」的合法形态
