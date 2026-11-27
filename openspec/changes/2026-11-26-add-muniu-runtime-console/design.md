# Design: 木牛服务管理台（runtime-console）

## Context

现有内嵌控制台含密钥/用量/供应商/价目/审计；共享控制将只读，managed 写走锦官；服务域配置与运行时仍需本地/standalone。

## Goals / Non-Goals

**Goals:**

- standalone 可运维；服务域配置可管理。
- managed 共享控制只读；锦官通过服务只读 API/投影只读展示。

**Non-Goals:**

- managed 不编辑共享控制。
- 不做通用 SQL。
- 不复制锦官控制台逻辑。

## Decisions

1. 页面分“服务域配置 / 运行时 / 共享控制只读”。
2. 服务域配置写入走服务 admin API；锦官只读视图走服务只读 API。
3. managed 共享控制只读；standalone 保留本地价目/拓扑/租户配置。
4. 本地运行命令直接执行；跨服务命令走锦官队列并显示 pending。
5. 服务/锦官不可用时 stale/pending；本地命令不受锦官影响。

## Risks / Trade-offs

- 与锦官职责重叠 → 共享控制只读/服务域配置服务写。
- 只读视图与内嵌台 UI 漂移 → 服务只读 API + 生成客户端 + 契约测试。
- 误操作 → 确认/审计/权限。

## Migration Plan

greenfield：拆分旧控制页；服务域配置/运行时保留；managed 共享控制只读。
