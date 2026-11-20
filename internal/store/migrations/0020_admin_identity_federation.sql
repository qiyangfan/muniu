-- 统一身份联邦(admin-operators):admin_users 记录联邦来源与锦官 sub,
-- 审计 actor_kind 值域增 federated。只增纪律:admin_users 只加列;
-- 审计表 CHECK 无法 ALTER,按 0012 同构重建并原样搬行(审计行只增不改,搬行不改内容)。
ALTER TABLE admin_users ADD COLUMN origin TEXT NOT NULL DEFAULT 'local' CHECK(origin IN ('local','federated'));
ALTER TABLE admin_users ADD COLUMN external_subject TEXT;
CREATE UNIQUE INDEX idx_admin_users_external_subject ON admin_users(external_subject) WHERE external_subject IS NOT NULL;

CREATE TABLE admin_audit_log_rebuilt (
 id TEXT PRIMARY KEY,
 request_id TEXT NOT NULL,
 actor_kind TEXT NOT NULL CHECK(actor_kind IN ('session','token','login','federated')),
 actor_name TEXT NOT NULL,
 action TEXT NOT NULL,
 target_id TEXT,
 outcome TEXT NOT NULL,
 detail TEXT,
 created_at TEXT NOT NULL
);
INSERT INTO admin_audit_log_rebuilt
 SELECT id,request_id,actor_kind,actor_name,action,target_id,outcome,detail,created_at FROM admin_audit_log;
DROP TABLE admin_audit_log;
ALTER TABLE admin_audit_log_rebuilt RENAME TO admin_audit_log;
CREATE INDEX idx_admin_audit_created ON admin_audit_log(created_at,id);
