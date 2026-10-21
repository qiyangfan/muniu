-- 按日对账 muniu 与 kongming 的用量。
-- 用法:sqlite3 data/muniu.db < scripts/reconcile.sql
-- 判据:两侧按日请求数/token 数差异 ≤1%;超限立案(postmortem)。
-- kongming 侧需以其自身用量表替换 kongming_usage_daily 视图/子查询后对齐口径。
--
-- 对账口径：请求按 request_id 去重；token 只计成功上游调用；缓存命中不计入。
-- 成本只计非 estimated 行；错误分类查询用于定位换道与故障。

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
