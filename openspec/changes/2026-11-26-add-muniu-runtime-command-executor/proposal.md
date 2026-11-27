# Proposal: 2026-11-26-add-muniu-runtime-command-executor（运行时命令执行器（command-executor））

## Why

锦官控制台需要在服务恢复后执行清缓存等跨服务命令；muniu 需要统一执行器。本地运行命令独立执行，不进入控制面队列。

## What Changes

- 以服务身份拉取/claim 跨服务命令；执行/ack。
- 缓存清理等有限动作；action/params schema/version 按 X-01 校验。
- 本地命令由内嵌控制台直接执行。
- 幂等/结果/审计；未知 action/版本拒绝。

## Non-goals

- 不执行任意配置/SQL。
- 不接受锦官直写数据面库。
- 不接管本地运行命令。
- 不做命令 UI。

## Capabilities

### New Capabilities

- `command-executor`：控制面命令的本地执行与回执

### Modified Capabilities

无

## Impact

admin/control 命令客户端/执行器；控制面命令队列；不写控制面。
