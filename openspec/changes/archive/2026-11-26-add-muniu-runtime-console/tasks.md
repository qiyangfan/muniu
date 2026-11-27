# Tasks

## 1. 控制台

- [x] 1.1 服务域配置页（key/供应商/OAuth/插件/余额/缓存）。验证：服务 admin API 写入、审计。（既有页面与 admin 审计保持；managed 仅共享控制只读）
- [x] 1.2 运行时页/只读共享控制。验证：managed 只读；standalone 本地配置可写。（Runtime 页 + 后端 403 + Pricing/ImagePricing 只读）
- [x] 1.3 本地命令 + 跨服务命令状态。验证：本地不受锦官影响；pending/acked/failed。（运行时页命令台账 + 本地执行）
- [x] 1.4 服务 admin API 仅本地写；服务只读 API 供锦官。验证：锦官无写入口；内嵌台写校验/落库/审计。（/service/v1/summary 只读脱敏，无写路由）

## 2. 权限

- [x] 2.1 scope/权限/审计。验证：GLOBAL/TENANT；共享控制写拒绝。（运行时路由 GLOBAL-only；managed 写 403）

## 3. 验收与独立运行

- [x] 3.1 权限负向与跨租户用例。验证：未授权 401/403，跨租户 404/不可见。（服务只读 API 匿名 401；既有 scope 测试）
- [x] 3.2 审计断言。验证：每次写操作有审计记录；普通列表只读不写审计；敏感详情/导出按审计策略。（命令 POST 走 admin 审计；GET 不写）
- [x] 3.3 独立运行/降级 smoke。验证：停掉管理面或其他仓，本服务核心流程不受影响；管理面停服仅局部不可用。（standalone 本地命令可用；managed last-known e2e）
- [ ] 3.4 契约与生成客户端。验证：OpenAPI/生成物无漂移；跨仓契约测试必跑（X-01）。本仓 OpenAPI 与生成类型已绿；**跨仓契约测试阻塞：X-01 未冻结。**
- [x] 3.5 Greenfield 重置。验证：无兼容/双轨分支。（managed 共享控制无本地写入口；旧控制拉取键拒绝）

## Workflow follow-up

- 归档前跑 `openspec validate --strict`；实现完成后按仓库门禁收口。
- 本 change 与 longzhong 聚合层 `docs/CONTROL-PLANE-PROJECT.md` 及 `docs/CONTROL-PLANE-BOUNDARY.md` 的对应 WP/边界表保持一致。
