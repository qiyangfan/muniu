-- 告警投递自监控(docs/design/balance-alerting.md 事件与通知节):
-- webhook 投递最终失败的事实单行持久化;失败期(episode)内重复失败推进计数与最近事实,
-- 任一次投递成功置解除并保留失败事实供追溯,新失败期开启时重置计数。
-- 递归纪律:该状态绝不经 webhook 外发(通道已断,外发即递归);历史 episode 不分行——
-- 消费方只关心「通道是否活着」与最近一次失败事实,明细归日志。
CREATE TABLE alert_delivery_status (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    failing INTEGER NOT NULL CHECK (failing IN (0,1)),
    last_event_key TEXT NOT NULL DEFAULT '',
    last_severity TEXT NOT NULL DEFAULT '',
    last_summary TEXT NOT NULL DEFAULT '',
    last_error TEXT NOT NULL DEFAULT '',
    failure_count INTEGER NOT NULL DEFAULT 0 CHECK (failure_count >= 0),
    last_failure_at TEXT NOT NULL DEFAULT '',
    last_resolved_at TEXT NOT NULL DEFAULT '',
    updated_at TEXT NOT NULL
);
