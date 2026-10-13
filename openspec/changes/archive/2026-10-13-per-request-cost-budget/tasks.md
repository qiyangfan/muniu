# Tasks: per-request-cost-budget

- [x] 1.1 失败测试先行:预检判定表测试(超界拒/零预算旁路/max_tokens 缺失旁路/负值 400)+ 流中中断测试(断言收尾/key 池零影响/计量落账)+ PATCH 链路
- [x] 1.2 迁移 0010(`max_request_cost_credits`)+ store/auth 接线(`KeyInfo.MaxRequestCostCredits` 进热路径)
- [x] 1.3 预检闸(handleChat 候选循环前)+ 流中闸(attemptStream/meterState)+ failover 判定表扩格(静默终结/不回写池)
- [x] 1.4 管理面 PATCH + 列表回显 + CLI(签发 flag/list 列)+ openapi.yaml 同步
- [x] 1.5 控制台:签发表单 + 行内编辑 + 列表展示
- [x] 1.6 文档:metering.md「请求级成本预算」节 + proxy-and-streaming 判定表扩格 + API key 设计节
- [x] 1.7 `make check` 全绿;archive + 主 specs 同步
