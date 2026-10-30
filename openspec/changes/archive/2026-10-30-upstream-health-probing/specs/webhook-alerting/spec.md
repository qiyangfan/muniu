# Spec Delta

## MODIFIED Requirements

### Requirement: 去重与事件覆盖

同一事件键在去重窗口内必须 (MUST) 只通知一次(窗口过期后再次发生重新通知);必须 (MUST) 覆盖三类运行时事件:候选全失败、provider key 池不可用、计量汇降级状态迁移,以及余额观测事件(余额低于绝对阈值、剩余时间穿越档位、余额源连续失败)与上游健康事件(渠道降级 `provider_degraded`、渠道恢复 `provider_recovered`、别名切换率越线 `switch_rate_exceeded`)——余额与上游健康事件由各自状态机判定触发时机并调用本管道发射;payload 必须 (MUST) 携带事件键、severity、摘要与发生时刻,余额观测与上游健康事件还必须 (MUST) 携带渠道名(切换率事件为别名)与触发上下文(余额值/剩余估算/失败计数/健康相位/换道率,按事件类型)。

#### Scenario: 去重窗口内静默

- **WHEN** 同一 provider 的 key 池不可用事件在窗口内连续发生
- **THEN** 仅首次发射 webhook,后续事件静默丢弃(不通知、不留痕)

#### Scenario: 窗口过期重新通知

- **WHEN** 同一事件键在去重窗口过期后再次发生
- **THEN** 重新发射通知

#### Scenario: 余额事件携带触发上下文

- **WHEN** 某渠道剩余估算穿越 3 天档位并发射告警
- **THEN** payload 除事件键/severity/摘要/时刻外,携带渠道名、当前有效余额与剩余估算值

#### Scenario: 健康事件携带触发上下文

- **WHEN** 某渠道探针连续失败进入降级态
- **THEN** payload 携带渠道名、健康相位与最近探针错误归类;恢复事件携带降级历时
