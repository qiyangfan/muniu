-- OAuth 订阅账号(oauth-subscription-lane 契约):订阅凭据落库,配置面仅持 account 引用。
-- refresh token 无法哈希(刷新需原文使用),明文落库是契约如实披露项(ADR-0021):
-- 缓解 = DB 文件权限与部署边界;state-backup 备份因此含敏感凭据,同样披露。
-- status: active | auth_failed(终态,重新授权恢复);时间列 RFC3339(UTC),与 balance 族同纪律。
CREATE TABLE IF NOT EXISTS oauth_accounts (
	id            TEXT PRIMARY KEY,
	provider_type TEXT NOT NULL,           -- 账号族:claude(Codex/gemini 为后续切片值域)
	status        TEXT NOT NULL,           -- active | auth_failed
	access_token  TEXT NOT NULL,
	refresh_token TEXT NOT NULL,
	expires_at    TEXT NOT NULL,           -- access token 过期时刻
	status_reason TEXT NOT NULL DEFAULT '',
	created_at    TEXT NOT NULL,
	updated_at    TEXT NOT NULL
);
