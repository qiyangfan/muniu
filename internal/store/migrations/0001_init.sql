-- 0001_init.sql — Schema v0.1(data-and-config.md 契约 DDL,逐字实现;禁改,只追加新迁移)
-- 时间列统一 TEXT RFC3339 UTC;金额列 INTEGER(μ 微单位);状态列 TEXT+CHECK 禁魔数。

CREATE TABLE schema_migrations (
  version    INTEGER PRIMARY KEY,
  applied_at TEXT NOT NULL
);

CREATE TABLE tenants (
  id         TEXT PRIMARY KEY,                 -- UUIDv7 字符串
  name       TEXT NOT NULL UNIQUE,
  status     TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','disabled')),
  created_at TEXT NOT NULL                      -- RFC3339 UTC
);
-- default 租户随迁移写入(种子 id 为固定 UUIDv7 字面量,保持迁移纯 SQL 且幂等确定)
INSERT INTO tenants (id, name, status, created_at)
VALUES ('01900000-0000-7000-8000-000000000001', 'default', 'active', '1970-01-01T00:00:00Z');

CREATE TABLE api_keys (
  id        TEXT PRIMARY KEY,
  tenant_id TEXT NOT NULL REFERENCES tenants(id),
  name      TEXT NOT NULL,
  key_hash  TEXT NOT NULL UNIQUE,               -- sha256 hex,明文绝不落盘
  prefix    TEXT NOT NULL,                      -- 明文前 12 字符(mk-XXXXXXXX)供列表辨认
  status    TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','revoked')),
  rpm_limit        INTEGER NOT NULL DEFAULT 60,
  tpm_limit        INTEGER NOT NULL DEFAULT 0,  -- 0=不限(Phase 3 生效硬闸)
  daily_quota_micro INTEGER NOT NULL DEFAULT 0, -- 0=不限;μ 单位见 metering
  currency         TEXT NOT NULL DEFAULT 'CNY' CHECK (currency IN ('CNY','USD')),
  created_at TEXT NOT NULL,
  revoked_at TEXT
);

CREATE TABLE usage_logs (
  id         TEXT PRIMARY KEY,
  request_id TEXT NOT NULL,
  tenant_id  TEXT NOT NULL,
  api_key_id TEXT NOT NULL,
  provider       TEXT NOT NULL,
  model_alias    TEXT NOT NULL,
  upstream_model TEXT NOT NULL,
  stream      INTEGER NOT NULL CHECK (stream IN (0,1)),
  status      TEXT NOT NULL CHECK (status IN ('ok','error','client_cancel')),
  error_class TEXT,
  prompt_tokens     INTEGER NOT NULL DEFAULT 0,
  completion_tokens INTEGER NOT NULL DEFAULT 0,
  reasoning_tokens  INTEGER NOT NULL DEFAULT 0,
  cached_tokens     INTEGER NOT NULL DEFAULT 0,
  estimated   INTEGER NOT NULL DEFAULT 0 CHECK (estimated IN (0,1)),
  cost_micro  INTEGER NOT NULL DEFAULT 0,
  currency    TEXT NOT NULL DEFAULT 'CNY',
  latency_first_token_ms INTEGER,
  latency_total_ms       INTEGER NOT NULL,
  created_at  TEXT NOT NULL
);
CREATE INDEX idx_usage_tenant_time ON usage_logs(tenant_id, created_at);
CREATE INDEX idx_usage_key_time    ON usage_logs(api_key_id, created_at);
CREATE INDEX idx_usage_day         ON usage_logs(created_at);   -- 日配额聚合
