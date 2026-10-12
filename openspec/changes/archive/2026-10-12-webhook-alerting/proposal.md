# Proposal: webhook-alerting

## Why

「生产基建」的完整度清单里 metrics/healthz/优雅停机/计量排干齐备,唯独告警缺位:候选全失败、key 池不可用、计量汇降级这些运营者必须知道的事件只有结构化日志,无人盯着日志就无人知晓。个人项目没有值班系统,恰是最需要主动通知的形态。

## What Changes

- 新增 `internal/alerting`:事件 → 去重窗口 → 退避重试 → webhook POST(JSON payload);通知失败绝不影响主路径(异步+超时+吞错留日志)。
- 接入三类事件:候选全失败(all_candidates_failed 终态)、provider key 池不可用(跳过候选时)、计量汇降级状态迁移。
- 配置 `alerts.webhook_url`(缺省关闭,不配置零开销)+ `alerts.dedup_window_sec`(缺省 300)。

## Capabilities

### Added Capabilities

- `webhook-alerting`:告警事件的通知语义(去重/重试/失败隔离)与事件覆盖面。
