-- T5.3 对账查询:muniu usage_logs 与 kongming 用量记录按日对账。
-- 用法:sqlite3 data/muniu.db < scripts/reconcile.sql
-- 判据:两侧按日请求数/token 数差异 ≤1%;超限立案(postmortem)。
-- kongming 侧需以其自身用量表替换 kongming_usage_daily 视图/子查询后对齐口径。
--
-- 对齐口径(与 metering 实现一致,勿凭直觉改):
--   * usage_logs 是「每次上游尝试一行」:failover 换道 N 次同一 request_id 落 N 行,
--     与 kongming(每请求一行)对账必须按 COUNT(DISTINCT request_id) 计数;
--   * token 只取终态成功行(status='ok',携带上游权威 usage),错误/断连行的 usage 为 0 或估算;
--   * 语义缓存命中行(provider='semantic-cache')未到达上游,kongming 侧无对应,两侧都应剔除;
--   * estimated 行 token 为本地估算,仅进参考列,不进对账 token 口径。

-- 1) muniu 侧按日汇总(对账口径:去重请求数 + 终态成功行 token;缓存命中/错误行单列参考)
SELECT substr(created_at, 1, 10)                AS day,
       COUNT(DISTINCT CASE WHEN provider != 'semantic-cache'
                           THEN request_id END)  AS requests,
       SUM(CASE WHEN status = 'ok' AND provider != 'semantic-cache'
                THEN prompt_tokens ELSE 0 END)   AS prompt_tokens,
       SUM(CASE WHEN status = 'ok' AND provider != 'semantic-cache'
                THEN completion_tokens ELSE 0 END) AS completion_tokens,
       SUM(CASE WHEN estimated = 0 THEN cost_credits ELSE 0 END) AS cost_credits,
       SUM(CASE WHEN provider = 'semantic-cache' THEN 1 ELSE 0 END) AS cache_hits,
       SUM(CASE WHEN status = 'error' THEN 1 ELSE 0 END)            AS errors
FROM usage_logs
WHERE tenant_id = (SELECT id FROM tenants WHERE name = 'kongming')
GROUP BY day
ORDER BY day;

-- 2) 错误分类 Top(换道/故障证据,事故复盘输入)
SELECT error_class, COUNT(*) AS n
FROM usage_logs
WHERE error_class IS NOT NULL
GROUP BY error_class
ORDER BY n DESC
LIMIT 20;
