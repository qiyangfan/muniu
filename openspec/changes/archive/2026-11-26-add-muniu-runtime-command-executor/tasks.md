# Tasks

## 1. 执行器

- [x] 1.1 跨服务命令 claim/执行/ack。验证：幂等/失败回执/服务身份。（TestCommandExecutor_幂等未知拒绝与本地命令）
- [x] 1.2 缓存清理 + 本地命令。验证：端到端；锦官不可用时本地仍可执行。（本地命令不经队列；e2e 命令台账）
- [x] 1.3 action schema/version 校验。验证：未知 action/版本、参数不合法 ack failed；不执行。（严格参数解析 + failed 回执）

## 2. 验收与独立运行

- [x] 2.1 权限负向与跨租户用例。验证：未授权 401/403，跨租户 404/不可见。（运行时命令 GLOBAL-only；服务身份失败退避）
- [x] 2.2 审计断言。验证：每次写操作有审计记录；普通列表只读不写审计；敏感详情/导出按审计策略。（本地命令走 admin 审计；跨服务命令 system 审计 + 台账）
- [x] 2.3 独立运行/降级 smoke。验证：停掉管理面或其他仓，本服务核心流程不受影响；管理面停服仅局部不可用。（本地命令直接执行测试）
- [ ] 2.4 契约与生成客户端。验证：OpenAPI/生成物无漂移；跨仓契约测试必跑（X-01）。**阻塞：X-01 未冻结。**
- [x] 2.5 Greenfield 重置。验证：无兼容/双轨分支。（无旧命令通道；本地命令与队列共用执行器但来源分明）

## Workflow follow-up

- 归档前跑 `openspec validate --strict`；实现完成后按仓库门禁收口。
- 本 change 与 longzhong 聚合层 `docs/CONTROL-PLANE-PROJECT.md` 及 `docs/CONTROL-PLANE-BOUNDARY.md` 的对应 WP/边界表保持一致。
