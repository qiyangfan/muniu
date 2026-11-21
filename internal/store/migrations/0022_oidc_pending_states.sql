-- 虎符登录 pending state 落库:重启/多副本不丢登录中请求,单次消费由删除保证。
-- state 只存 SHA-256 哈希;verifier/nonce 为 10 分钟短期凭据,随行删除失效。
CREATE TABLE IF NOT EXISTS oidc_pending_states (
  state_hash TEXT PRIMARY KEY,
  verifier   TEXT NOT NULL,
  nonce      TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_oidc_pending_states_created ON oidc_pending_states(created_at);
