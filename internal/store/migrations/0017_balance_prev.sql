-- 异常消耗告警(docs/design/balance-alerting.md 核心算法与纪律):
-- provider_balances 追加「上一次快照」三列,成功写入时平移——骤降判定(cliff:<key_id> 规则)
-- 的判据是相邻两次成功快照的余额转移:prev_* 恒为「最近一次相邻转移」的前值。
-- 只追加迁移;失败查询不动前值(失败不产生新转移),首次成功写入前 prev_* 为 NULL(无转移可判)。
ALTER TABLE provider_balances ADD COLUMN prev_balance_credits INTEGER;
ALTER TABLE provider_balances ADD COLUMN prev_currency TEXT NOT NULL DEFAULT '';
ALTER TABLE provider_balances ADD COLUMN prev_as_of_at TEXT NOT NULL DEFAULT '';
