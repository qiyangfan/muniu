# 租户级管理员:管理面范围收敛(GLOBAL / TENANT)

> 目标授权形态「IdP 粗角色 + 产品侧细范围」的第一次真实落地。跨仓决策见
> `longzhong/docs/adr/0002-authorization-strategy.md` 与 `architecture/IAM-TARGET.md`;
> OPA 对照(Spike B)与 Keycloak 角色映射不在本件,本件只留单一决策点 seam。

## Why

木牛的租户轴只存在于数据面:api key 绑定 tenant、用量行携带 tenant_id、租户停用即拒鉴权;
管理面仍是"能认证即可管全部"。这不是漏写的过滤器,是缺失的一条授权轴——管理主体没有
"我只能管这些租户"的表达。

目标形态至今没有实例:孔明 `data_scope` 是 SQL 轴的应用内自建引擎,锦官是 `USER`/`ADMIN`
二元角色,都不能替代对「粗角色在 IdP、细范围在产品/策略层」的验证。木牛是最低成本的真实宿主:
tenants / api_keys / usage_logs 已有 tenant 维度,管理面路由是单一事实源,且本来就是
Keycloak pilot 的下一棒。

不做锦官:钱包/账本/主体没有租户归属,半权管理员必须先做产品建模(钱包归租户还是归人),
不在本变更里虚构角色。

## What Changes

- `admin_principals` 增 `scope`(`GLOBAL|TENANT`,显式列);新表 `admin_principal_tenants`
  (subject ↔ tenant_id,FK `tenants`,整包替换语义)。
- 管理面路由表增分类声明(global-only / tenant-scoped + 租户提取);scope 中间件串入
  `auth(audit(scope(mux)))`;未分类路由默认 global-only,scoped 主体 fail-closed。
- 租户资源按绑定收敛:`keys`(列表/签发/调整/吊销)、`usage`(强制租户集过滤)、
  `tenants`(只读本人范围);providers / pricing / topology / plugins / balances /
  admins / audit 仍 global-only。
- 契约纯增:`whoami` 与 `admins` 列表增 scope 与绑定;`PATCH /admin/v1/admins/{subject}`
  纯增范围授予(仅 GLOBAL actor 可授予);scope 拒绝入审计。
- 共享管理令牌 = GLOBAL break-glass,语义不变;存量主体迁移为 GLOBAL(行为兼容),
  新建主体默认 TENANT 空绑定(失败关闭)。
- console 按 scope 过滤导航与操作,显示范围徽标;数据面零改动。

## Capabilities

### Added Capabilities

- `admin-tenant-scope`:范围模型与绑定、路由分类与单点执行、读写收敛、fail-closed、契约纯增。

### Modified Capabilities

- `admin-operators`:鉴权通过 ≠ 全路由放行;主体契约增 scope;审计覆盖 scope 拒绝;新主体默认 TENANT。

## Non-goals

- OPA 接入与策略编写(Spike B;本件只留 seam)。
- Keycloak / 统一身份 client 角色映射(独立接入变更;范围数据由产品侧管理)。
- 锦官任何改动;钱包/账本租户归属建模。
- 数据面(api key 鉴权、限流、计量、调度)零触碰。
- 通用权限点 / RBAC 平台、租户自助管理台、按租户计费策略。
- `muniu admins` CLI 扩展(授权先走管理 API;CLI 待 Keycloak 角色映射一并评估)。

## 影响与确认

- 对外契约:管理 API 纯增字段;GLOBAL actor(含共享令牌、存量主体)行为不变;
  新建 TENANT 主体在获得授权前对所有管理路由 403(`whoami` / `logout` / 认证入口除外)。
- 数据:新列 + 新表,存量 backfill GLOBAL;greenfield 可重置;无数据面迁移。
- 运维:首个 GLOBAL 范围的授予经共享令牌(管理 API)完成,避免新库"无人可授权"死锁。
- 验收:负向矩阵(跨租户、空绑定、未分类路由)+ 全局轨零回归 + 审计留痕。
