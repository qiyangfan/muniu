-- 0004_model_pricing.sql — 运行态价目(动态改价,metering.md「动态价目」)。
-- 价目是运营态数据(厂商改价=运维动作):管理 API/控制台在线增改删,即时生效零重启;
-- YAML pricing 降级为静态兜底(本表未覆盖的别名回落 YAML,见装配层合并语义)。禁改 0001-0003,只追加。
CREATE TABLE model_pricing (
  alias               TEXT PRIMARY KEY,        -- 对外模型别名(与 pricing 静态表同键空间)
  currency            TEXT NOT NULL,           -- CNY | USD
  input_micro         INTEGER NOT NULL,        -- μ/百万Token(ParsePrice 换算后的整数形态)
  cached_input_micro  INTEGER NOT NULL DEFAULT 0, -- 0=缺省同 input(CostWithCached 语义)
  output_micro        INTEGER NOT NULL,
  timezone            TEXT NOT NULL DEFAULT 'UTC', -- "UTC" 或 ±HH:MM 固定偏移
  time_windows        TEXT NOT NULL DEFAULT '[]',  -- JSON [{"window":"HH:MM-HH:MM","basis_points":5000}]
  updated_at          TEXT NOT NULL            -- RFC3339 UTC
);
