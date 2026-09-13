-- 0006_api_key_expiry.sql — api_keys 增过期时间(gateway-parity「key 生命周期」):
-- 可空 DATETIME(TEXT RFC3339 UTC);NULL = 永不过期(现状等价,存量行零迁移语义变化)。
-- 过期判定在鉴权层以当前时钟逐请求执行(时间函数,不依赖本列的索引/触发器)。
ALTER TABLE api_keys ADD COLUMN expires_at TEXT;
