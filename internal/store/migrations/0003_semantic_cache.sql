-- 0003_semantic_cache.sql — 语义缓存表与租户级缓存开关。
-- 禁改旧迁移,只追加。
ALTER TABLE tenants ADD COLUMN semantic_cache_enabled INTEGER NOT NULL DEFAULT 0; -- 0=关(默认关,契约)

CREATE TABLE semantic_cache (
  id          TEXT PRIMARY KEY,                 -- UUIDv7 字符串
  tenant_id   TEXT NOT NULL REFERENCES tenants(id),
  fingerprint TEXT NOT NULL,                    -- SHA256(归一化指纹) hex
  model_alias TEXT NOT NULL,
  embedding   BLOB NOT NULL,                    -- 嵌入向量 float32[] 小端
  response    BLOB NOT NULL,                    -- 命中回放的 OpenAI 响应 JSON
  hits        INTEGER NOT NULL DEFAULT 0,
  created_at  TEXT NOT NULL,                    -- RFC3339 UTC
  expires_at  TEXT NOT NULL                     -- RFC3339 UTC,TTL 默认 24h
);
CREATE UNIQUE INDEX idx_sc_tenant_fp ON semantic_cache(tenant_id, fingerprint); -- 同租户同指纹唯一(覆盖写)
CREATE INDEX idx_sc_expires   ON semantic_cache(expires_at);
