CREATE TABLE admin_users (
 id TEXT PRIMARY KEY,
 username TEXT NOT NULL UNIQUE,
 password_hash TEXT NOT NULL,
 status TEXT NOT NULL DEFAULT 'active' CHECK(status IN ('active','disabled')),
 created_at TEXT NOT NULL,
 last_login_at TEXT
);
CREATE TABLE admin_sessions (
 token_hash TEXT PRIMARY KEY,
 user_id TEXT NOT NULL REFERENCES admin_users(id),
 created_at TEXT NOT NULL,
 expires_at TEXT NOT NULL
);
CREATE INDEX idx_admin_sessions_user ON admin_sessions(user_id);
CREATE TABLE admin_audit_log (
 id TEXT PRIMARY KEY,
 request_id TEXT NOT NULL,
 actor_kind TEXT NOT NULL CHECK(actor_kind IN ('session','token','login')),
 actor_name TEXT NOT NULL,
 action TEXT NOT NULL,
 target_id TEXT,
 outcome TEXT NOT NULL,
 detail TEXT,
 created_at TEXT NOT NULL
);
CREATE INDEX idx_admin_audit_created ON admin_audit_log(created_at,id);
