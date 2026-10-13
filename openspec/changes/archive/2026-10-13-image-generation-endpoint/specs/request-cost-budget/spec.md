# Spec Delta

## MODIFIED Requirements

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
