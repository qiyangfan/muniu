# Proposal: per-request-cost-budget

## Why

现有成本治理全部是「事后」的:日配额按已落账成本拦截,请求一旦开始,失控补全(死循环长生成、超长上下文误投)没有任何闸门,直到上游账单落地才可见。请求级成本预算把成本治理前移到「单请求」粒度:预检阶段用「估算输入成本 + max_tokens 输出上界」拒绝必然超预算的请求(上游零调用),流式阶段按累计成本中断失控生成(已发生部分如实计量)。这是主流网关的成本治理标配(LiteLLM max_budget 语义),也是 muniu 计量计费叙事从「记账准确」升级到「成本可控」的关键一环——求职项目的差异化能力,不是等消费者才做的功能。

## What Changes

- 迁移 0010:`api_keys.max_request_cost_credits INTEGER NOT NULL DEFAULT 0`(0=不限;单请求成本上界,credit 单位与日配额同源)。
- 预检闸:chat 管道(四条入站 lane 共用)进候选循环前,预算>0 且价目可得且 `max_tokens`>0 时,估算输入成本 + max_tokens 输出上界,超界 → 402 `cost_budget_exceeded`,上游零调用、不落计量行(与 strict 治理 400 同族)。
- 流式中断闸:流式转发中按累计成本(输入=usage 帧真值优先/估算兜底;输出=delta 增量估算与 usage 帧累计取先到)检查,超界 → 已开闸走流内错误帧干净收尾(客户端已收部分保留),未开闸回 402 信封;已发生部分如实计量落账(`error_class=cost_budget_exceeded`)。
- key 治理面:PATCH/CLI/控制台全链暴露该字段;`KeyInfo` 携带进热路径,写后 Invalidate 即时生效。
- failover 判定表增格:`cost_budget_exceeded` = 网关策略类,静默终结——不计 key 失败(不冷却不禁用)、不换道。

## Capabilities

### Added Capabilities

- `request-cost-budget`:请求级成本预算的预检判定、流式中断与治理面语义。
