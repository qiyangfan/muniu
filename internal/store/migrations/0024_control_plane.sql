-- 0024_control_plane.sql — 控制面快照 last-known 与运行时命令台账。
-- 快照表只存最后成功应用的整包(含签名)，重启后先按 last-known 起服再拉新。
-- 命令表是本地执行幂等台账：control=锦官队列来源，local=内嵌控制台直接执行。
CREATE TABLE control_snapshots (
  service_id  TEXT PRIMARY KEY,
  version     INTEGER NOT NULL,
  etag        TEXT NOT NULL DEFAULT '',
  observed_at TEXT NOT NULL,
  signature   TEXT NOT NULL,
  payload     TEXT NOT NULL,
  updated_at  TEXT NOT NULL
);

CREATE TABLE control_commands (
  id                     TEXT PRIMARY KEY,
  action                 TEXT NOT NULL,
  action_schema_version  INTEGER NOT NULL,
  params                 TEXT NOT NULL DEFAULT '',
  source                 TEXT NOT NULL CHECK (source IN ('control','local')),
  status                 TEXT NOT NULL CHECK (status IN ('pending','acked','failed')),
  result                 TEXT NOT NULL DEFAULT '',
  attempts               INTEGER NOT NULL DEFAULT 0,
  idempotency_key        TEXT NOT NULL DEFAULT '',
  actor                  TEXT NOT NULL DEFAULT '',
  reason                 TEXT NOT NULL DEFAULT '',
  created_at             TEXT NOT NULL,
  completed_at           TEXT
);
CREATE INDEX idx_control_commands_created ON control_commands(created_at);

-- 审计 actor_kind 值域增 system：控制快照应用/系统命令执行需要非人类归因。
DROP INDEX IF EXISTS idx_admin_audit_created;
ALTER TABLE admin_audit_log RENAME TO admin_audit_log_old;
CREATE TABLE admin_audit_log (
 id TEXT PRIMARY KEY,
 request_id TEXT NOT NULL,
 actor_kind TEXT NOT NULL CHECK(actor_kind IN ('session','token','login','federated','system')),
 actor_name TEXT NOT NULL,
 action TEXT NOT NULL,
 target_id TEXT,
 outcome TEXT NOT NULL,
 detail TEXT,
 created_at TEXT NOT NULL
);
INSERT INTO admin_audit_log (id,request_id,actor_kind,actor_name,action,target_id,outcome,detail,created_at)
 SELECT id,request_id,actor_kind,actor_name,action,target_id,outcome,detail,created_at FROM admin_audit_log_old;
DROP TABLE admin_audit_log_old;
CREATE INDEX idx_admin_audit_created ON admin_audit_log(created_at,id);
