# request-cost-budget Specification

## Purpose
单请求粒度的成本闸:预检拒绝必然超预算的请求(上游零调用),流式中断失控生成(已发生部分如实计量),把成本治理从「事后对账」前移到「事中拦截」。预算挂在 key 治理面,0=不限(现状等价,零回归)。

## Requirements

### Requirement: 预检闸(可计算即拒绝)

key 配置单请求成本预算(>0)且请求成本上界可计算时,管道必须 (MUST) 在触达上游前判定;超预算必须 (MUST) 以 402 `cost_budget_exceeded` 拒绝,上游零调用且不落计量行。预算为 0(缺省)时必须 (MUST) 零开销旁路,行为与引入预算前逐字节一致。成本上界的计算形态按通道分族:chat 管道(四条入站 lane 共用)在价目可得且 `max_tokens`>0 时估算「输入成本 + max_tokens 输出上界」(保守取整,缓存折扣不计);images 生成通道在价目可得时以「n × 有效档位价」为精确成本(无输出不确定性,不存在诚实旁路缺口)。chat 通道 `max_tokens` 缺失或价目不可得时预检不拒绝(上界不可知,不做伪保护),该限制在文档如实登记。

#### Scenario: 超预算预检拒绝

- **WHEN** 以配置了预算的 key 发起「估算输入 + max_tokens 计价上界」超预算的 chat 请求
- **THEN** 返回 402 `cost_budget_exceeded`,上游零调用,usage_logs 无该请求行

#### Scenario: 预算缺省零回归

- **WHEN** key 未配置预算(0)发起任意请求
- **THEN** 行为与引入预算前逐字节一致,既有测试零改动通过

#### Scenario: max_tokens 缺失诚实旁路

- **WHEN** 预算>0 但 chat 请求未携带 `max_tokens`(非流式)
- **THEN** 预检不拒绝(输出上界不可知,不做伪保护),该限制在文档如实登记

#### Scenario: images 精确预检

- **WHEN** 预算>0 的 key 发起 images 请求,且「n × 有效档位价」超预算
- **THEN** 返回 402 `cost_budget_exceeded`,上游零调用,usage_logs 无该请求行;images 价目缺失时不拒绝(成本不可知语义与无价目记零一致)

### Requirement: 流式中断(超限即断言收尾)

翻译出站路径的流式请求累计成本超预算时必须 (MUST) 中断:已开闸(客户端已收到内容帧)走流内错误帧(`cost_budget_exceeded`)并干净收尾,已收部分保留;未开闸返回 402 `cost_budget_exceeded` 信封。直通 passthrough 流必须 (MUST) 在未开闸时同样拦截(402 信封),已开闸段不强制中断(无原生协议错误帧合成机制,登记限制)。已发生的输入与部分输出必须 (MUST) 如实计量落账(`status=error`、`error_class=cost_budget_exceeded`,usage 按真值/估算现行口径)。该中断必须 (MUST) NOT 计入 key 健康度(不冷却、不禁用、不记 EMA),必须 (MUST) NOT 触发候选换道。

#### Scenario: 流中超限断言收尾

- **WHEN** 翻译路径流式转发中累计成本超过预算
- **THEN** 客户端收到流内 `cost_budget_exceeded` 错误帧与干净收尾,此前已收内容帧保留

#### Scenario: 直通流未开闸拦截

- **WHEN** 直通 passthrough 流在开闸前累计成本超预算
- **THEN** 返回 402 `cost_budget_exceeded` 信封,客户端零内容帧污染

#### Scenario: 中断不计 key 失败

- **WHEN** 同一 key 连续多次触发流中预算中断
- **THEN** key 池状态保持 healthy(无冷却/禁用),后续请求正常路由

#### Scenario: 中断计量如实落账

- **WHEN** 流中预算中断发生且上游已回传 usage 帧
- **THEN** usage_logs 落一行,error_class=cost_budget_exceeded,token 与成本按已发生部分记录

### Requirement: 治理面与热路径

预算字段必须 (MUST) 经 `PATCH /admin/v1/keys/{id}`(`max_request_cost_credits`,负值拒绝)、CLI(签发与列表)与控制台可设可清(0=不限);写后必须 (MUST) 立即生效(鉴权缓存失效协议);列表必须 (MUST) 回显当前值。

#### Scenario: PATCH 即时生效

- **WHEN** 将某 key 预算从 0 改为 N 后立即以该 key 发起超预算请求
- **THEN** 预检立即按 N 拒绝(无重启、无等待缓存 TTL)

#### Scenario: 字段回显

- **WHEN** 签发携带预算的 key 后查询列表与控制台
- **THEN** 该条目回显预算值,0 如实显示为「不限」
