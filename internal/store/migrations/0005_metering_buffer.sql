-- 0005_metering_buffer.sql — 计量本地缓冲(ADR-0007):ingest 汇失败时整批落本地,恢复后续传。
-- 缓冲行以自增 seq 保序;远端以 row_id 幂等去重;超容量阻塞消费(背压传导,计量行绝不丢)。禁改旧迁移,只追加。
CREATE TABLE metering_buffer (
  seq        INTEGER PRIMARY KEY AUTOINCREMENT,
  row_id     TEXT NOT NULL,              -- UsageRow 主键(UUIDv7;远端幂等键)
  payload    TEXT NOT NULL,              -- UsageRow JSON
  created_at TEXT NOT NULL               -- RFC3339 UTC
);
