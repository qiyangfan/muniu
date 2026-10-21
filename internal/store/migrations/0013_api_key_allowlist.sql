-- 0013_api_key_allowlist.sql — key 级模型白名单。
-- allowed_models = 模型别名 JSON 数组 TEXT;NULL/空数组 = 不限(存量行语义不变)。
-- 白名单按别名(客户端可见路由键)约束数据面访问;运营态,经 admin PATCH 调整,
-- 写后使鉴权缓存失效。不校验别名存在性:拓扑热替换下允许先于别名签发。
ALTER TABLE api_keys ADD COLUMN allowed_models TEXT;
