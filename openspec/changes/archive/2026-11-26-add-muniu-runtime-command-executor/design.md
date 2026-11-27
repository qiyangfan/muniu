# Design: 运行时命令执行器（command-executor）

## Context

锦官跨服务命令队列是新的；muniu 已有缓存清理能力。本地运行命令不应被锦官队列绑架。

## Goals / Non-Goals

**Goals:**

- 跨服务命令可执行/回执。
- 服务恢复后收敛；本地命令独立可用。

**Non-Goals:**

- 不做任意配置。
- 不接管本地运行命令。
- 不做 UI。

## Decisions

1. 只接受有限命令枚举；按 X-01 action/params schema/version 校验；未知 action/版本拒绝；拒绝任意 SQL/配置写入。
2. 以服务身份 claim/执行/ack；幂等键；失败重试上限。
3. 执行结果审计。
4. 本地命令走内嵌控制台，与方法 1-3 共用执行器但不经队列。

## Risks / Trade-offs

- 重复执行 → 幂等。
- 长任务超时 → 状态/上限。
- 双通道 → 来源标注 + 统一审计。

## Migration Plan

greenfield：新增命令执行器；本地命令保留。
