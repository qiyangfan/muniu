# Design: error-evidence-scope

## Context

见 proposal.md「Why」。现有 failover 判定表只有「换道/断言/返回/静默」四类动作；keypool 与 health 各自解释 `ErrorClass`，导致证据跨层污染。候选迭代器是 provider 级，每个候选只取一次 key。

## Goals / Non-Goals

**Goals:**

- 一张表统一错误证据的消费权：谁可以因这个错误改变状态。
- 同 provider 多 key 在 key 级错误下有界重试。
- 保持首 token 后断言、400 不换道、客户端断连静默等既有语义。

**Non-Goals:**

- 不改变首 token/帧间超时模型。
- 不做跨模型降级或并行对冲。
- 不改变 key 池权重算法与冷却阶梯数值。

## Decisions

1. **三列判定表而非单一 scope 标签**。`ErrorClass` 映射为 `{keySignal, channelSignal, attemptAction}`：例如 429 = `{cooldown, none, retry_same_provider_key}`；5xx = `{consecutive_cooldown, failure, next_candidate}`；400 = `{none, none, return}`；404/unknown = `{none, none, next_candidate}`；dial/timeout = `{ema_failure, failure, next_candidate}`。理由：5xx 同时需要 key 本地快速保护与渠道级熔断，单一标签表达不了；三列能逐格测试且不丢现有语义。
2. **同 provider 换 key 只对 key 级硬信号开放**（429/401/403/欠费）。换 key 通过再次 `Pick` 实现（冷却键已自然过滤），次数受 failover 总预算约束；换 key 不增加别名换道计数，不写渠道被动窗口。5xx/网络错误直接下一候选，避免在故障渠道内逐个 key 打满。
3. **404/未知 4xx 归候选级**：允许换下一候选（不同 provider 的 model 映射可能有效），但不写 key EMA、不写渠道窗口、不写冷却。
4. **400 维持直接返回**：参数问题换家也拒；同时不再写 key EMA（这是本变更的行为修正）。
5. **5xx 保留 key 连续计数冷却**：它是同一 key 的快速本地保护，成本低；渠道级熔断仍由 health 承担。若后续实测显示多 key 同渠道被逐个冷却的副作用大于收益，再单独收敛。
6. **契约变更**：尝试序列与换道计数可观测变化，实施前补 ADR，并更新 `proxy-and-streaming.md` 判定表。

## Risks / Trade-offs

- [同 provider 换 key 放大尝试次数] → 受 failover 总预算/最大尝试数约束；只对快速失败类（429/401/402）开放。
- [429 不再降渠道，单 key 渠道会持续 429 直到冷却] → key 冷却后池空，走池耗尽信封；渠道级容量问题由 `keypool_exhausted` 告警暴露。
- [三列表与现有判定表重复] → 实施时以新表替换旧分类映射，旧表只保留首 token 后动作，避免双源。
- [health.enabled=false 时失去渠道信号] → 关闭 health 本就是回到 key 级行为；key 级 5xx 冷却仍保留。

## Migration Plan

无存储迁移。先补 ADR 与判定表测试矩阵，再改消费点；上线后观察换道率、429 与 `all_candidates_failed` 比例。回滚按配置/提交回退，无数据兼容问题。
