-- 0009_api_key_disable.sql — api_keys 增可逆停用时刻(key-disable-toggle):
-- NULL = 启用(现状等价,存量行零迁移语义变化);非空 = 停用(可逆,启用即清列)。
-- 吊销(status='revoked')仍是唯一终态,本列对其无意义;判定顺序先吊销后停用。
-- 可逆运行态不并入 status CHECK:SQLite 扩 CHECK 需整表重建,风险与收益不成比例。
ALTER TABLE api_keys ADD COLUMN disabled_at TEXT;
