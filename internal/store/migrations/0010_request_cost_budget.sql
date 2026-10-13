-- 0010_request_cost_budget.sql — api_keys 增单请求成本预算(per-request-cost-budget):
-- 0 = 不限(缺省,现状等价零回归);>0 = 单请求成本上界(credit,与日配额同单位同源)。
-- 执行链:预检闸(估算输入+max_tokens 上界,超界 402 上游零调用)+ 流式中断闸(累计成本超界断言收尾)。
ALTER TABLE api_keys ADD COLUMN max_request_cost_credits INTEGER NOT NULL DEFAULT 0;
