# Tasks

## 1. 工具与审计

- [x] 1.1 独立 CLI/凭证/时间窗。验证：无凭证/过期拒绝。（无 token/错误 token/过期窗口/权限过宽均拒绝；凭证失败前不连库）
- [x] 1.2 原因/二次确认/审计摘要。验证：可追责。（原因必填、确认取消不执行、审计含 reason/sql_summary/rows）

## 2. 运行手册

- [x] 2.1 文档化修复场景与回滚。验证：演练一次。（docs/runbook-break-glass.md + 自动化演练测试）
- [x] 2.2 负向：控制台无该通道。验证：端点不存在。（无 admin 路由；仅独立 cmd/muniu-breakglass）

## 3. 验收与独立运行

- [x] 3.1 权限负向与跨租户用例。验证：未授权 401/403，跨租户 404/不可见。（独立凭证负向测试；不暴露管理面）
- [x] 3.2 审计断言。验证：每次写操作有审计记录；普通列表只读不写审计；敏感详情/导出按审计策略。（修复与审计同事务；审计失败回滚）
- [x] 3.3 独立运行/降级 smoke。验证：停掉管理面或其他仓，本服务核心流程不受影响；管理面停服仅局部不可用。（CLI 直连 SQLite，不依赖网关/控制面）
- [x] 3.4 契约与生成客户端。验证：不适用（本 change 无跨仓契约）。
- [x] 3.5 Greenfield 重置。验证：无兼容/双轨分支。（新增独立工具，无旧通道）

## Workflow follow-up

- 归档前跑 `openspec validate --strict`；实现完成后按仓库门禁收口。
- 本 change 与 longzhong 聚合层 `docs/CONTROL-PLANE-PROJECT.md` 及 `docs/CONTROL-PLANE-BOUNDARY.md` 的对应 WP/边界表保持一致。
