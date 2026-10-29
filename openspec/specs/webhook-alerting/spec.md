# webhook-alerting Specification

## Purpose
运营关键事件(key 池不可用/候选全失败/计量汇降级)的主动通知:事件→去重→退避重试→webhook,让无人值守形态下「网关出事有人知道」。

## Requirements

### Requirement: 告警通知不干扰主路径

告警发射必须 (MUST) 异步且带超时;webhook 不可达/非 2xx 必须 (MUST) 按退避重试后放弃并留日志,必须 (MUST) NOT 阻塞、拖慢或失败化任何数据面请求;未配置 webhook_url 时告警子系统必须 (MUST) 完全关闭(零开销)。

#### Scenario: webhook 宕机请求照常

- **WHEN** webhook 端点持续不可达期间数据面请求到达
- **THEN** 请求正常服务,重试按退避进行并最终放弃留日志

#### Scenario: 未配置零开销

- **WHEN** 配置无 alerts.webhook_url
- **THEN** 不发起任何通知行为,数据面行为与本变更前逐字节一致

### Requirement: 去重与事件覆盖

同一事件键在去重窗口内必须 (MUST) 只通知一次(窗口过期后再次发生重新通知);必须 (MUST) 覆盖三类运行时事件:候选全失败、provider key 池不可用、计量汇降级状态迁移,以及余额观测事件(余额低于绝对阈值、剩余时间穿越档位、余额源连续失败)——后者由余额告警状态机判定触发时机并调用本管道发射;payload 必须 (MUST) 携带事件键、severity、摘要与发生时刻,余额观测事件还必须 (MUST) 携带渠道名与触发上下文(余额值/剩余估算/失败计数,按事件类型)。

#### Scenario: 去重窗口内静默

- **WHEN** 同一 provider 的 key 池不可用事件在窗口内连续发生
- **THEN** 仅首次发射 webhook,后续事件静默丢弃(不通知、不留痕)

#### Scenario: 窗口过期重新通知

- **WHEN** 同一事件键在去重窗口过期后再次发生
- **THEN** 重新发射通知

#### Scenario: 余额事件携带触发上下文

- **WHEN** 某渠道剩余估算穿越 3 天档位并发射告警
- **THEN** payload 除事件键/severity/摘要/时刻外,携带渠道名、当前有效余额与剩余估算值
