# Design: 木牛运行时 break-glass（break-glass）

## Context

muniu 单二进制 + SQLite；控制面/网关分离后，运行时修复不能走控制台常规写路径。

## Goals / Non-Goals

**Goals:**

- 应急修复可控、可审计。
- 不成为第二写者/日常路径。

**Non-Goals:**

- 不做 UI。
- 不做任意 SQL 控制台。
- 不替代 HA/备份。

## Decisions

1. 独立二进制/脚本，独立存储凭证；不随控制台部署。
2. 每次操作需原因、二次确认、时间窗与审计记录。
3. 只允许文档化修复场景；常规修复走管理 API/命令。

## Risks / Trade-offs

- 高危凭证泄露 → 独立保管/限时/审计。
- 误操作 → 备份 + 二次确认 + 影响行数上限。

## Migration Plan

greenfield：新增工具；无数据迁移。
