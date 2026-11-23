# admin-tenant-scope Specification

## Purpose

管理面范围收敛:范围模型(`GLOBAL`/`TENANT`)、租户绑定生命周期、路由分类与单点执行、
租户资源的读写收敛、范围不可确定时 fail-closed、契约纯增。数据面授权(api key→tenant)
不归本域;OPA 与 IdP 角色映射不在本域。

## Requirements

### Requirement: 范围模型与绑定

管理主体的管理范围必须 (MUST) 是显式枚举 `GLOBAL|TENANT`(`admin_principals.scope`);
禁止 (MUST NOT) 以「是否存在租户绑定」推断范围——缺省不得折算为全局。`TENANT` 主体的
租户集合必须 (MUST) 存于 `admin_principal_tenants`(subject, tenant_id, FK `tenants`),
写入必须 (MUST) 为整包替换语义。范围与绑定的变更仅 `GLOBAL` actor 可执行;
`scope=GLOBAL` 携带非空租户集合必须 (MUST) 400 拒绝;`scope=TENANT` 允许空集合
(待授权态,执行侧 fail-closed);未知 `scope` 取值必须 (MUST) 400 拒绝。共享管理令牌轨
必须 (MUST) 恒为 `GLOBAL` break-glass,不参与本表的读写。

#### Scenario: 授予租户范围

- **WHEN** GLOBAL actor 对主体 S 提交 `scope=TENANT` 与 `tenant_ids=[t1,t2]`
- **THEN** 落库并整包生效,主体列表回显 S 的 scope 与两个租户

#### Scenario: 非 GLOBAL 不得授予

- **WHEN** TENANT actor 调用范围授予接口
- **THEN** 403,不写库,留审计 denied

#### Scenario: GLOBAL 携带绑定被拒

- **WHEN** 提交 `scope=GLOBAL` 且 `tenant_ids` 非空
- **THEN** 400 整单拒绝,不落库,不静默忽略绑定字段

#### Scenario: 令牌轨恒全局

- **WHEN** 以共享管理令牌调用管理端点
- **THEN** 按 `GLOBAL` 判定,不受任何主体绑定约束,行为与变更前一致

### Requirement: 路由分类与单点执行

管理面路由必须 (MUST) 在单一事实源(路由表)声明分类:`global-only` 或 `tenant-scoped`
(附租户提取);未分类路由必须 (MUST) 按 `global-only` 处理,禁止 (MUST NOT) 对 `TENANT`
主体默认放行。范围判定必须 (MUST) 收敛在单一决策点;拒绝必须 (MUST) 产生 403 并进入
审计 denied。`TENANT` 主体访问 `global-only` 路由必须 (MUST) 403;`GLOBAL` actor
(共享令牌与存量主体)的行为必须 (MUST) 与变更前一致。

#### Scenario: scoped 访问 global-only 被拒

- **WHEN** TENANT 主体调用 `GET /admin/v1/providers` 或 `GET /admin/v1/audit`
- **THEN** 403,不触达资源处理器

#### Scenario: 未分类路由默认拒绝 scoped

- **WHEN** 新增一条未声明分类的管理路由并被 TENANT 主体调用
- **THEN** 按 `global-only` 处理并拒绝,而非默认放行

#### Scenario: GLOBAL 零回归

- **WHEN** GLOBAL actor 调用任一既有管理端点
- **THEN** 状态码、响应体与变更前一致,既有测试零改动通过

### Requirement: 读侧范围收敛

`GET /admin/v1/keys`、`GET /admin/v1/tenants`、`GET /admin/v1/usage` 必须 (MUST) 对
`TENANT` 主体收敛到绑定租户集:列表只返回绑定租户的行;`usage` 的 `tenant_id` 参数必须
(MUST) 在绑定集内,缺省返回绑定并集(以 `tenant_id IN (...)` 聚合,禁止 (MUST NOT) 循环
单租户查询拼接);范围外参数必须 (MUST) 按 `404 not_found` 拒绝,禁止 (MUST NOT) 静默
忽略后返回空集或并集。审计查询(`GET /admin/v1/audit`)保持 `global-only`。

#### Scenario: 列表只含绑定租户

- **WHEN** TENANT 主体(绑定 t1)查询 key 列表与租户列表
- **THEN** 只返回 tenant=t1 的 key 与 t1 本身,其余租户的行不可见

#### Scenario: 越界查询被拒

- **WHEN** 绑定 t1 的主体以 `tenant_id=t2` 查询 usage
- **THEN** 404 not_found,不留痕为"已执行",审计记 denied

#### Scenario: 多绑定并集

- **WHEN** 绑定 [t1,t2] 的主体不带参数查询 usage
- **THEN** 返回 t1 与 t2 的聚合,且只经一次租户集合过滤

#### Scenario: 显式参数必须是子集

- **WHEN** 绑定 [t1,t2] 的主体以 `tenant_id=t1` 查询 usage
- **THEN** 正常返回 t1 的聚合,参数生效且不放大范围

### Requirement: 写侧范围收敛

签发 key 的 `tenant` 必须 (MUST) 在绑定集内,范围外必须 (MUST) 按 `404 tenant_not_found`
拒绝且不落库;调整/吊销必须 (MUST) 以目标 key 的租户判定,范围外必须 (MUST) 按
`404 not_found` 拒绝、不写库、不产生任何副作用。租户创建/修改与 providers / pricing /
topology / plugins / balances 全部保持 `global-only`。全部拒绝必须 (MUST) 写审计 denied。

#### Scenario: 越权签发被拒

- **WHEN** 绑定 t1 的主体以 `tenant=t2` 签发 key
- **THEN** 404 tenant_not_found,无 key 落库

#### Scenario: 越权调整与吊销被拒

- **WHEN** 绑定 t1 的主体对 t2 的 key 调用 PATCH 或 DELETE
- **THEN** 404 not_found,key 原样保留,无审计之外的可观察副作用

#### Scenario: 合法写生效

- **WHEN** 绑定 t1 的主体以 `tenant=t1` 签发 key,并对其调整限额
- **THEN** 行为与 GLOBAL actor 执行同操作一致,立即生效

### Requirement: 范围不可确定时 fail-closed

`TENANT` 主体绑定为空、主体行不存在或已停用、`scope` 取值不在值域内时,除认证入口、
`whoami`、`logout` 外,管理面请求必须 (MUST) 拒绝(认证面 401 / 权限面 403),禁止
(MUST NOT) 退化为「看全部」,也禁止 (MUST NOT) 以空列表冒充已授权。禁用主体语义不变:
存量会话下一请求即 401。

#### Scenario: 空绑定主体只能看自己

- **WHEN** 新建 TENANT 主体(空绑定)完成登录后调用任一管理端点
- **THEN** 仅 `whoami`/`logout` 可达,其余 403,响应不复用"空集=有权"的表达

#### Scenario: 未知 scope 拒绝

- **WHEN** 主体行的 scope 被旁路写入未知值
- **THEN** 除认证入口外全部拒绝,审计记录拒绝原因

#### Scenario: 停用主体仍 401

- **WHEN** 已停用主体持存量会话请求管理端点
- **THEN** 401,与既有停用语义一致

### Requirement: 契约纯增

`GET /admin/v1/auth/whoami` 必须 (MUST) 纯增 `subject`(令牌轨为空串)、`scope`、
`tenant_ids`;`GET /admin/v1/admins` 必须 (MUST) 纯增 `scope` 与 `tenant_ids`;
`PATCH /admin/v1/admins/{subject}` 必须 (MUST) 纯增可选 `scope`/`tenant_ids`(省略=不动,
整包替换)。既有字段名、状态码与语义不得改变;旧客户端可忽略新增字段;OpenAPI 契约漂移
门禁必须 (MUST) 覆盖新增字段。

#### Scenario: 旧客户端不传新字段

- **WHEN** 旧客户端以仅含 `disabled` 的请求体调用主体 PATCH
- **THEN** 行为与变更前一致,scope/绑定保持不变

#### Scenario: 整包替换

- **WHEN** 对绑定 [t1,t2] 的 TENANT 主体提交 `tenant_ids=[t2]`
- **THEN** 绑定变为 [t2],t1 立即不可见

#### Scenario: 显式空集撤销

- **WHEN** 提交 `scope=TENANT` 且 `tenant_ids=[]`
- **THEN** 全部租户绑定清空,主体进入待授权态,执行侧 fail-closed

#### Scenario: 契约漂移门禁

- **WHEN** 运行契约导出与漂移门禁
- **THEN** openapi 与前端类型包含新增字段且无未提交差异
