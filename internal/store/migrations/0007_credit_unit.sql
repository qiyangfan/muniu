-- 0007_credit_unit.sql — 计量单位从 μ(1e-6 本币)切换为 credit(1 credit = 本位币 1e-9)。
-- 四个金额列重命名并按 ×1000 换算存量值(1 μ = 1000 credit),保证新旧数值语义连续。
ALTER TABLE api_keys RENAME COLUMN daily_quota_micro TO daily_quota_credits;
ALTER TABLE tenants RENAME COLUMN daily_quota_micro TO daily_quota_credits;
ALTER TABLE usage_logs RENAME COLUMN cost_micro TO cost_credits;

UPDATE api_keys SET daily_quota_credits = daily_quota_credits * 1000 WHERE daily_quota_credits > 0;
UPDATE tenants SET daily_quota_credits = daily_quota_credits * 1000 WHERE daily_quota_credits > 0;
UPDATE usage_logs SET cost_credits = cost_credits * 1000 WHERE cost_credits > 0;

-- 价目表同基座换算(0004 的三列亦为 μ 刻度)
ALTER TABLE model_pricing RENAME COLUMN input_micro TO input_credits;
ALTER TABLE model_pricing RENAME COLUMN cached_input_micro TO cached_input_credits;
ALTER TABLE model_pricing RENAME COLUMN output_micro TO output_credits;
UPDATE model_pricing SET input_credits = input_credits * 1000, cached_input_credits = cached_input_credits * 1000, output_credits = output_credits * 1000;
