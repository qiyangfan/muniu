# Proposal: responses-state-delete-proxy

## Why

responses 有状态 lane 已有粘性路由+GET 亲和代理,DELETE 是 OpenAI Responses 状态面的第三个动词;网关代理了创建与读取却不能代删,故事线悬空,客户端只能绕过网关直连上游清理状态。

## What Changes

- `DELETE /v1/responses/{id}`:亲和已知 → 经 NativeFetcher 代理转发亲和 provider(状态/头/体零改写);亲和未知 → 恒 404 `response_not_found`(与 GET 同规)。
- `NativeFetcher.PrepareNativeFetch` 扩展方法参数(GET/DELETE 共用同一出站构造)。

## Capabilities

### Added Capabilities

(无——并入既有 protocol-native-passthrough 能力)

### Modified Capabilities

- `protocol-native-passthrough`:新增「状态 lane 读删代理」Requirement(GET 既有行为一并钉死 + DELETE 新行为)。
