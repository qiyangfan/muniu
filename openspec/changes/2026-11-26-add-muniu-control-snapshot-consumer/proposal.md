# Proposal: 2026-11-26-add-muniu-control-snapshot-consumer（控制快照消费者（control-snapshot-consumer））

## Why

muniu 的价目、拓扑、吊销、租户/配额等共享控制状态改由锦官拥有；当前拉取各自为政，缺统一快照/版本/认证语义。

## What Changes

- 新增统一控制快照拉取、签名校验、缓存与版本收敛；服务身份认证。
- 价目/拓扑/吊销/租户/配额/共享策略包按快照应用。
- managed 下关闭本地价目/拓扑/租户第二写者；standalone last-known/本地配置。

## Non-goals

- 不写控制面。
- 不做控制面 UI。
- 不改数据面转发协议。

## Capabilities

### New Capabilities

- `control-snapshot-consumer`：控制快照拉取、缓存与 last-known 语义

### Modified Capabilities

无

## Impact

admin/control 拉取与运行目录；数据面热路径读本地快照；不写控制面。
