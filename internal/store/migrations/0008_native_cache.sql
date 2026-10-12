-- 0008_native_cache.sql — 语义缓存直通双存(protocol-native-passthrough):
-- 直通条目在 canonical chat 形态(既有 response 列,跨协议命中)之外并存原生响应体 +
-- 入站协议标签 + 表单标记(同构命中零翻译恒等回放)。三列皆可空 = 翻译路径条目形态不变。
-- 禁改既有迁移,只追加。
ALTER TABLE semantic_cache ADD COLUMN native_response BLOB;                  -- 直通原始响应体(逐字节)
ALTER TABLE semantic_cache ADD COLUMN native_proto TEXT NOT NULL DEFAULT ''; -- 入站协议标签(messages|responses)
ALTER TABLE semantic_cache ADD COLUMN native_form   TEXT NOT NULL DEFAULT '';-- 表单标记(stream|nonstream)
