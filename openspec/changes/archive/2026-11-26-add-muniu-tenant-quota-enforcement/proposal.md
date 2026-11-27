# Proposal: 2026-11-26-add-muniu-tenant-quota-enforcement（租户级配额执行（traffic-governance））

## Why

租户级配额归锦官拥有；muniu 按快照执行，并与 key 级配额叠加。managed 下租户/配额本地只读，standalone 保留本地配置。

## What Changes

- 从控制快照读取租户级 RPM/TPM/日配额。
- 与 key 级配额叠加判定。
- 缺失/非法 fail-closed；审计。

## Non-goals

- 不拥有租户配额定义。
- 不改 key 级配额语义。
- 不做分布式强一致计数。

## Capabilities

### New Capabilities

无

### Modified Capabilities

- `traffic-governance`：控制面租户配额快照的本地执行

## Impact

限流网关/计数；控制快照消费；不写控制面。
