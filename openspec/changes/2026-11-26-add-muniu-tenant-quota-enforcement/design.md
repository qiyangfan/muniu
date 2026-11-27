# Design: 租户级配额执行（traffic-governance）

## Context

现有 traffic-governance 只有 key 级 RPM/TPM/日配额；租户配额由锦官下发。managed 下本地租户/配额是只读投影。

## Goals / Non-Goals

**Goals:**

- 租户上限可执行。
- key 级语义不变。

**Non-Goals:**

- 不做租户配额定义/管理。
- 不做跨集群强一致。

## Decisions

1. 租户配额从锦官快照读取；0=不限。
2. 租户与 key 配额同时判定，任一超限拒绝。
3. 快照缺失/非法时安全类 fail-closed。
4. managed 下关闭本地租户/配额写入；standalone 保留本地配置。

## Risks / Trade-offs

- 租户计数成本 → 复用计数后端；管理面量级可控。
- 快照陈旧 → stale 提示/保守执行。

## Migration Plan

greenfield：从快照加载租户配额。
