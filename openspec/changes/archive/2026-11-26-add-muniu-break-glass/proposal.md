# Proposal: 2026-11-26-add-muniu-break-glass（木牛运行时 break-glass（break-glass））

## Why

控制面/网关不可用时仍需受控修复运行时存储；该通道不能嵌入控制台，也不能成为日常写路径。

## What Changes

- 提供独立 CLI/工具，直连 muniu 运行时存储。
- 独立凭证、原因、二次确认、审计、限时。
- 仅限文档化修复操作；不提供 admin 端点。

## Non-goals

- 不做日常写路径。
- 不嵌入木牛控制台。
- 不绕过计量/鉴权不变量作为常规方案。
- 不提供任意用户可用的 SQL 控制台。

## Capabilities

### New Capabilities

- `break-glass`：运行时存储的独立 break-glass 修复通道

### Modified Capabilities

无

## Impact

新增独立 CLI/工具与运行手册；不新增 admin 端点；不修改数据面 API。
