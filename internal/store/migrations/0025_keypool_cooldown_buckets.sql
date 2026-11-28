-- keypool 冷却桶独立计时(ADR-0025):一 key 多桶各一行,主键加入 bucket。
-- 重建表以变主键;存量单行天然对应一个桶,复制后删旧表。
CREATE TABLE IF NOT EXISTS key_cooldowns_new (
	provider    TEXT NOT NULL,
	key_id      TEXT NOT NULL,
	bucket      TEXT NOT NULL,
	cool_until  TEXT NOT NULL,
	consec      INTEGER NOT NULL DEFAULT 0,
	ev_seq      INTEGER NOT NULL DEFAULT 0,
	updated_at  TEXT NOT NULL,
	PRIMARY KEY (provider, key_id, bucket)
);

INSERT INTO key_cooldowns_new (provider, key_id, bucket, cool_until, consec, ev_seq, updated_at)
SELECT provider, key_id, bucket, cool_until, consec, ev_seq, updated_at FROM key_cooldowns;

DROP TABLE key_cooldowns;

ALTER TABLE key_cooldowns_new RENAME TO key_cooldowns;
