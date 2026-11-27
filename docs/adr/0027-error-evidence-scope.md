# ADR-0027:上游错误证据作用域与同 provider 换 key 重试

- 状态:已接受(2026-11-27;openspec `error-evidence-scope` 承载)
- 背景:keypool 与渠道健康各自解释 `ErrorClass`。429/401 会进入渠道被动窗口与半开裁决,单 key 限频可以熔断整渠道;400/404 会进入 key EMA,请求/候选级错误被当成 key 质量问题。同一 provider 有多把 key 时,一次 429 只换下一个 provider,不换同 provider 的健康 key。
- 关联:ADR-0019(渠道健康)、ADR-0026(换道预算)、openspec `traffic-governance`/`upstream-health`/`keypool-scheduling`

## 决策

统一用一张三列表:每个 `ErrorClass` 明确 `keySignal`、`channelSignal`、`attemptAction`。证据只允许被指定消费者读取。

| ErrorClass | keySignal | channelSignal | attemptAction |
| --- | --- | --- | --- |
| dial_fail / TLS | EMA 失败 | failure | next_candidate |
| timeout_first | EMA 失败 | failure | next_candidate |
| upstream_5xx | 连续计数冷却 + EMA | failure | next_candidate |
| rate_limited_429 | 冷却 + EMA | none | retry_same_provider_key |
| auth_failed_401/403 | 禁用 + EMA | none | retry_same_provider_key |
| 欠费(402/insufficient_quota) | 欠费冷却 | none | retry_same_provider_key |
| bad_request_400 | none | none | return |
| unknown 4xx(404/422 等) | none | none | next_candidate |
| mid_stream_fail(首 token 前) | EMA 失败 | failure | next_candidate |
| mid_stream_fail(首 token 后) | EMA 失败 | none | abort |
| context_canceled / cost_budget | none | none | silent |

1. **同 provider 换 key 只对 key 级硬信号开放**:429/401/403/欠费时再次 `Pick` 同 provider 的下一可用 key(冷却键自然过滤),次数受 ADR-0026 的 `max_attempts` 与 deadline 约束;换 key 不计别名换道率、不计渠道被动窗口。
2. **渠道级错误直接换候选**:dial/TLS/首 token 超时/5xx/首 token 前断流不走同 provider 换 key,避免在故障渠道内逐个 key 打满。
3. **5xx 保留 key 连续计数冷却**:作为同 key 的快速本地保护;渠道级熔断仍由 health 承担。
4. **400/404 不再污染 key 与渠道**:400 维持不换道直接返回;404/未知 4xx 允许换下一候选(不同 provider 的 model 映射可能有效),但不写 EMA/冷却/禁用、不写渠道窗口。
5. **证据回填跟随实际路径**:只有进入换道/半开试探的动作才回填渠道证据;首 token 后断言出口不回填渠道被动窗口。
6. **契约变更**:尝试序列、换道计数与 key/渠道状态可观测变化;实施时以本表替换现有分类映射,并更新 `docs/design/proxy-and-streaming.md` 判定表。

## 备选与否决

- 保留单一 scope 标签:5xx 同时需要 key 快速保护与渠道熔断,单标签表达不了——否决。
- 5xx 也同 provider 换 key:会在故障渠道内横向打满所有 key——否决。
- 400/404 继续记 key EMA:请求级错误被当成 key 质量问题,多 key 池被无谓降权——否决。
- 429 继续计入渠道被动窗口:单 key 限频可熔断整渠道,与 key 池归因矛盾——否决。

## 后果

- `internal/failover`、`internal/proxy`、`internal/keypool`、`internal/health` 的消费点按表调整;判定表全格测试。
- 同 provider 换 key 会增加尝试次数,必须与 ADR-0026 的预算同批实施。
- `health.enabled=false` 时失去渠道信号属预期;key 级 5xx 冷却与 EMA 仍保留。
