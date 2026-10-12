# Tasks: webhook-alerting

- [x] 1.1 alerting 单测(去重窗口/退避重试后放弃/失败隔离/payload 形态/nil 零开销);顺带修 deliver 末次判定下标错位(只试 2 次即放弃)
- [x] 1.2 Notifier 实现与配置面(alerts.webhook_url/dedup_window_sec)
- [x] 1.3 三接入点:候选全失败/key 池不可用/sink 降级回调
- [x] 1.4 make check 全绿;archive + 主 specs 同步
