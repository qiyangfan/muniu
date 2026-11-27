# Design: 共享控制快照消费者（control-snapshot-consumer）

## Context

现有 muniu 分别拉取价目/拓扑/吊销；没有统一快照契约、认证与 last-known 语义。本地还有价目/拓扑/租户写入口，managed 下会形成第二写者。

## Goals / Non-Goals

**Goals:**

- 共享控制统一收敛。
- 锦官缺席仍可运行。
- managed 无第二写者。

**Non-Goals:**

- 不做锦官存储。
- 不下发服务域配置。
- 不做实时推送。

## Decisions

1. 统一快照 envelope：版本/ETag/observed_at/按服务投影。
2. 服务身份 + 签名校验；应用原子替换；旧版本不回退。
3. 安全类缺失 fail-closed；非安全类 last-known + stale。
4. managed 关闭本地价目/拓扑/租户写入口；standalone 保留本地配置。
5. 服务域配置（key/凭据/插件/缓存）不走快照。

## Risks / Trade-offs

- 快照应用半途失败 → 原子替换/旧目录保留。
- 版本回退 → 单调版本。
- 本地覆盖残留 → managed 校验 + 负向测试。

## Migration Plan

greenfield：删除分散拉取；managed 关闭本地共享控制写入；standalone 保留本地配置。
