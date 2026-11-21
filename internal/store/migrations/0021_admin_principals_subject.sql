-- 木牛去账号化(ADR-0010):虎符 sub 成为唯一管理员主体键。
-- 零存量重建:旧表含本地账密行(origin='local' 或存在 password_hash)时迁移 abort,
-- 构建期无法统一告警,故由装配期启动闸 store.EnsureNoLocalAdminAccounts 给出明确错误。
-- 旧表 admin_users 保留(迁移只增不改,不 DROP):联邦影子 id 列不再被读写。
CREATE TABLE IF NOT EXISTS admin_principals (
  subject       TEXT PRIMARY KEY,             -- 虎符规范化 sub(issuer+sub 绑定),身份唯一键
  username      TEXT NOT NULL,                -- 展示名,来自 id_token name;不参与身份判定
  status        TEXT NOT NULL DEFAULT 'active' CHECK(status IN ('active','disabled')),
  created_at    TEXT NOT NULL,
  last_login_at TEXT
);
CREATE INDEX IF NOT EXISTS idx_admin_principals_username ON admin_principals(username);

-- 旧会话按影子账号 id 归属,无法反推 subject;会话是 12 小时短期凭据,整体作废由重新登录恢复。
DELETE FROM admin_sessions;
CREATE TABLE admin_sessions_rebuilt (
  token_hash TEXT PRIMARY KEY,
  subject    TEXT NOT NULL REFERENCES admin_principals(subject),
  created_at TEXT NOT NULL,
  expires_at TEXT NOT NULL
);
DROP TABLE admin_sessions;
ALTER TABLE admin_sessions_rebuilt RENAME TO admin_sessions;
CREATE INDEX idx_admin_sessions_subject ON admin_sessions(subject);
