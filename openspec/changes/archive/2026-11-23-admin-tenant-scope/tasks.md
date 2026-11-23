# Tasks

## 1. 存储与迁移

- [x] 1.1 迁移 0023:`admin_principals.scope`(`GLOBAL|TENANT`,默认 `TENANT`)+ `admin_principal_tenants`(subject↔tenant_id,FK `tenants`)+ 存量 backfill `GLOBAL`。验证:迁移往返测试 + backfill 行级断言 + 新插入默认 TENANT
- [x] 1.2 store 访问器:范围/绑定读写(整包替换)、主体列表带 scope/绑定、按租户集过滤的 key/tenant/usage 查询(IN)。验证:表驱动 store 测试(SQLite 临时库)全绿

## 2. 决策点与中间件

- [x] 2.1 【测试先行】路由表分类:条目增 global-only / tenant-scoped + 租户提取;未分类默认 global-only。验证:覆全 `adminRouteTable` 的分类断言测试(新增路由漏分类即变红)
- [x] 2.2 【测试先行】scope 中间件串入 `auth(audit(scope(mux)))`;actor 上下文携带 subject/scope/绑定的租户;拒绝产生 403 并入审计 denied(含 GET)。验证:链序断言 + 403 留痕 + 401 零回归
- [x] 2.3 单一决策函数 `AuthorizeScope(actor, class, resourceTenant)`;资源处理器只消费判定。验证:执行点只经该函数(grep 断言)+ 单测覆盖 allow/deny 两态

## 3. 资源收敛

- [x] 3.1 keys:列表按绑定租户过滤;签发租户必须在范围;调整/吊销按目标 key 的租户判定,越界 404 且不写。验证:跨租户签/改/吊销负向用例 + 审计行
- [x] 3.2 usage:强制绑定租户集(缺省=并集,显式参数必须为子集),越界 404;聚合 `tenant_id IN (...)`。验证:多绑定并集 / 越界 / 空绑定三组合
- [x] 3.3 tenants:列表只读本人范围;create/patch 保持 global-only(路由类)。验证:范围矩阵用例
- [x] 3.4 admins:列表纯增 scope/绑定;PATCH 纯增 `scope`/`tenant_ids`(整包替换,仅 GLOBAL 授予);400 校验(未知租户 / GLOBAL 带绑定 / 未知 scope)。验证:契约用例与负向用例全绿

## 4. 契约与控制台

- [x] 4.1 openapi 与 api-types 同步(漂移门禁);`whoami` 纯增 subject/scope/tenant_ids。验证:契约漂移门禁绿
- [x] 4.2 console:按 `whoami.scope` 过滤导航——GLOBAL 保持全部页签;TENANT 仅 Usage/Keys 并默认落地 Usage(Overview 的缓存统计与告警投递是 global-only 源,403 后静默降级为「未启用/无告警」会误导,整页不纳入 TENANT 视图)。Keys 页租户输入改从 `GET /tenants` 下拉(读侧已按 scope 收敛,前端零授权逻辑);范围徽标 + 403 走既有 ErrorAlert;不做租户页与绑定编辑界面。验证:375/768/1280 × 亮暗走查 + 裸元素自检 + TENANT 主体导航无隐藏页

## 5. 门禁与回归

- [x] 5.1 【测试先行】scope 矩阵表测:路由 × actor(GLOBAL/TENANT 空绑定/TENANT 多绑定/令牌)× 租户。验证:全绿,含未分类路由与停用主体用例
- [x] 5.2 零回归:共享令牌与存量 GLOBAL 主体行为不变;数据面测试套零改动通过。验证:既有测试原样全绿
- [x] 5.3 `make check` 全绿 + 迁移往返 + OpenSpec 校验(CLI 可用时 `openspec validate --all --strict`)。验证:提交 b32f9f5 后全量 `make check` EXIT=0(vet/lint 0 issues/deadcode/licenses/secrets/doclink/infra-lint/tidy-check/web-check 含 api-types 漂移翻转/make test race+覆盖率 74.9%/plugin-check 70.45%/spec-check);`openspec validate --all --strict` 31/31 通过

## Workflow follow-up

- OPA 对照(Spike B)以本切片的 `AuthorizeScope` 为宿主替换实现;聚合层 `SPIKE-PLAN` 的候选宿主条目随之更新(owner 文档,不在本仓任务)
- 评审迭代循环已完成(零已证实缺陷,1 项风险转 HANDOFF 开放决策项);openspec 归档于 2026-11-23 执行
- git 提交等 owner 逐次指令
