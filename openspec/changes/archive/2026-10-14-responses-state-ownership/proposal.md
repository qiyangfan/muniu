# Proposal: responses-state-ownership

## Why

状态 lane（GET/DELETE `/v1/responses/{id}` 与 `previous_response_id` 粘性）的亲和条目只有 `id→provider` 维度、无归属判定：任何持有效 key 的请求方拿到 respID 即可读回他 key 创建的响应内容、**不可逆删除**他 key 的上游存储、以他 key 的 prev_id 续写其服务端上下文。kongming 实际部署为单租户多用户、每用户一把 key——这是真实的跨用户数据隔离洞（含不可逆操作），key-model-allowlist 评审循环溯源确认（2026-10-13 开放项登记升格裁决：修）。

## What Changes

- 亲和条目增归属维度：`affEntry` 增 `apiKeyID`，`Put` 于成功响应回写时以创建请求的鉴权 key 记录（mk- 与 mk2- 同规，KeyInfo.ID 即归属标识）。
- 三处消费点归属校验，不匹配**≡「未知」同语义**（不可区分，不泄露存在性）：`previous_response_id` 粘性 → `response_affinity_lost` 自解释降级（与亲和未知同码同文案族）；GET → 404 `response_not_found`；DELETE → 404 `response_not_found` 且**上游零调用**（绝不出站删他 key 状态）。
- 鉴权信息缺失（生产不可达——auth 中间件保证）按 fail-closed 同「未知」处理，不留无主后门。
- 粒度裁决 = **per-key**：kongming 单租户形态下 per-tenant 无隔离意义；key 轮换后旧响应状态不可续用（24h TTL 内的自然边界，安全默认优先）。

## Capabilities

### New Capabilities

（无——归属是 protocol-native-passthrough 既有状态 lane 语义的收紧，不另立能力。）

### Modified Capabilities

- `protocol-native-passthrough`：修改「有状态粘性路由」与「状态读删代理」两条 requirement——亲和命中增归属判定前置，不匹配按未知语义处理。

## Impact

- **代码**：`internal/proxy/affinity.go`（条目结构/Put/Get 签名）、`internal/proxy/responses.go`（HandlerResponsesStateless）、`internal/proxy/proxy.go`（handleChat 成功回写 Put + stickyRoute 消费）。
- **契约**：GET/DELETE 他 key respID 从「代理转发」变为 404（安全收紧，行为面变更已在裁决中声明）；同 key 全链零变化。
- **测试先行域**（铁律 2）：跨 key GET/DELETE/prev_id 三拒绝（断言上游零调用）+ 同 key 零变化回放。
