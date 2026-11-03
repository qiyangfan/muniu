-- keypool 冷却持久化(keypool-credit-bucket 契约):
-- 仅 ≥5min 的冷却迁移落库(欠费桶全程/429 长冷却),重启恢复+SIGHUP 热替换无损;
-- 瞬态(5xx 30s/429 前段)不落库防写放大。bucket: credit | rate;清除以空 bucket 传参即删行
-- (欠费回写「充值即恢复」的提前释放走同一条路径)。
CREATE TABLE IF NOT EXISTS key_cooldowns (
	provider    TEXT NOT NULL,
	key_id      TEXT NOT NULL,
	bucket      TEXT NOT NULL,
	cool_until  TEXT NOT NULL,           -- RFC3339(UTC),与 balance 族时间列同纪律
	consec      INTEGER NOT NULL DEFAULT 0,
	ev_seq      INTEGER NOT NULL DEFAULT 0, -- 事件单调序号:锁外落库乱序守卫(迟到的旧事件被忽略)
	updated_at  TEXT NOT NULL,
	PRIMARY KEY (provider, key_id)
);
