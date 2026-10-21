-- 0002_tenant_quota.sql — 租户级日配额与 TPM 限额列。
-- 禁改旧迁移,只追加;SQLite ALTER ADD COLUMN 带 NOT NULL 必须有非空默认值。
ALTER TABLE tenants ADD COLUMN daily_quota_micro INTEGER NOT NULL DEFAULT 0; -- 0=不限;μ 单位见 metering
ALTER TABLE tenants ADD COLUMN tpm_limit        INTEGER NOT NULL DEFAULT 0; -- 0=不限
