# Design: webhook-alerting

- `internal/alerting.Notifier`:Emit(eventKey, severity, summary);内部 map 去重(带窗口)、3 次退避重试(100ms/400ms/1.6s)、POST 超时 3s;goroutine 化,fire-and-forget。
- 接入点三处(最小面):①proxy 候选耗尽 all_candidates_failed 出口;②proxy key 池 Pick nil 跳过分支;③metering sink degraded 状态迁移(经 Config 注入回调,避免 metering 反向依赖 alerting——Sink 生命周期已有状态位,迁移沿置回调)。
- 去重粒度=eventKey(含 provider 维度,如 `keypool_exhausted:p1`);并发安全 mutex;进程内形态,集群共享按 ADR-0008 惯例登记演进。
- payload:`{"event":...,"severity":...,"summary":...,"occurred_at":RFC3339}`;Content-Type application/json。
