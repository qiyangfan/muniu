# Proposal: 2026-11-26-add-muniu-usage-health-reporter（用量与健康上报（data-plane-metering-sink））

## Why

锦官需要 muniu 的 LLM 用量/健康来计费、执行权益与展示服务状态；LLM 用量以 muniu 为唯一来源。

## What Changes

- 扩展 metering sink 推锦官；服务身份认证。
- 断网缓冲/重试/幂等。
- 健康/版本/能力上报。
- 固定口径：LLM token/费用只由 muniu 上报。

## Non-goals

- 不做计费。
- 不改数据面计量口径。
- 不上报请求内容/凭据。
- 不让其他服务重复上报 LLM 用量。

## Capabilities

### New Capabilities

无

### Modified Capabilities

- `data-plane-metering-sink`：控制面用量/健康上报

## Impact

metering sink/缓冲；控制面 ingest；不写控制面之外的数据。
