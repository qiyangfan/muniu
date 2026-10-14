-- 0013: key 级模型白名单(key-model-allowlist)。
-- allowed_models = 模型别名 JSON 数组 TEXT;NULL/空数组 = 不限(现状等价,零回归)。
-- 白名单按别名(客户端可见路由键)约束数据面访问;运营态,经 admin PATCH 随时调整
-- (写后走鉴权缓存 Invalidate 同款协议)。不校验别名存在性:拓扑热替换下允许先于别名签发。
ALTER TABLE api_keys ADD COLUMN allowed_models TEXT;
