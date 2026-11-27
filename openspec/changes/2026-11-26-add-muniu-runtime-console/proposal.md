# Proposal: 2026-11-26-add-muniu-runtime-console（木牛运行时控制台（runtime-console））

## Why

共享控制迁到锦官后，muniu 仍需要服务域配置、standalone 运行时诊断、本地命令与只读投影；锦官通过服务只读 API/投影只读展示。

## What Changes

- 内嵌控制台保留服务域配置页：key 签发/池、供应商凭据、OAuth、插件、余额、缓存。
- 新增/保留运行时页：健康/日志/命令/投影。
- managed 下租户/价目/拓扑/配额/共享策略只读；standalone 保留本地配置。
- 本地运行命令直接执行；跨服务命令显示 pending/acked/failed。
- 服务 admin API 供内嵌控制台写；服务只读 API 供锦官只读视图。

## Non-goals

- managed 下不编辑租户/价目/拓扑/共享策略。
- 不直连锦官/服务 DB。
- 不替代锦官控制台。
- 不接管共享控制写。

## Capabilities

### New Capabilities

- `runtime-console`：muniu 内嵌运行时控制台

### Modified Capabilities

无

## Impact

muniu/web 内嵌控制台；命令客户端；不写控制面。
