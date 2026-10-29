-- 渠道余额观测(见 docs/design/balance-alerting.md):
-- provider_balances 每渠道每 key 至多一行最新快照(key_id='__account__' 为账户级源),
-- balance_alert_states 每渠道每规则一行告警状态;历史曲线不落库,日耗由 usage_logs 现算。
-- balance_credits 以余额币种 1e-9 刻度整数存储,与 cost_credits 同纪律;NULL = 余额不可知
-- (openrouter 不限额键、尚未成功查询)。允许负值:欠费是真实信号(硅基流动赠送口径透支、
-- 阿里云账务欠费均返回负数),钳为 0 反而丢掉「已透支」语义。as_of_at 截断为秒级 RFC3339 UTC,
-- 与 usage_logs.created_at 同形态,字符串序比较成立。
CREATE TABLE provider_balances (
    provider TEXT NOT NULL,
    key_id TEXT NOT NULL,
    source TEXT NOT NULL,
    balance_credits INTEGER,
    currency TEXT NOT NULL DEFAULT '',
    as_of_at TEXT NOT NULL DEFAULT '',
    manual_amount TEXT,
    last_error TEXT,
    consecutive_failures INTEGER NOT NULL DEFAULT 0 CHECK (consecutive_failures >= 0),
    updated_at TEXT NOT NULL,
    PRIMARY KEY (provider, key_id)
);

CREATE TABLE balance_alert_states (
    provider TEXT NOT NULL,
    rule_key TEXT NOT NULL,
    firing INTEGER NOT NULL CHECK (firing IN (0,1)),
    severity TEXT NOT NULL DEFAULT '',
    last_fired_at TEXT,
    context_json TEXT,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (provider, rule_key)
);
