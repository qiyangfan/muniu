# Spec Delta

## MODIFIED Requirements

### Requirement: 管理员主体与会话

管理员主体必须 (MUST) 以虎符 sub 为唯一键(`admin_principals.subject` 主键);展示名来自
id_token `name`,必须 (MUST) 不参与身份判定(同名不同 sub 可并存)。系统必须 (MUST NOT)
接受任何本地口令登录,不得保存 `password_hash` 作为登录凭据。登录成功必须 (MUST) 签发
HttpOnly 会话 cookie(服务端只存令牌哈希,固定有效期),注销必须 (MUST) 立即失效该会话。
会话校验必须 (MUST) 逐请求确认主体仍活跃——主体停用后其全部存量会话必须 (MUST) 即时失效
(下一请求即 401),启用必须 (MUST NOT) 恢复旧会话。主体只能由虎符首登自动建档;管理必须
(MUST) 提供按 subject 的列表/停用/启用(管理 API 与 `muniu admins` CLI)。

主体必须 (MUST) 携带管理范围 `GLOBAL|TENANT`(范围模型与执行归 `admin-tenant-scope`),
新建主体必须 (MUST) 默认 `TENANT`;主体列表必须 (MUST) 回显 scope 与租户绑定。
范围的授予与变更仅 `GLOBAL` actor 可执行,且不改变身份键与停用语义。

#### Scenario: 登录签发会话

- **WHEN** 活跃主体经虎符 OIDC 授权码 + PKCE 完成登录
- **THEN** 返回会话 cookie(HttpOnly)与操作员身份,后续带 cookie 的管理面请求获准

#### Scenario: 本地账密不可用

- **WHEN** 向 `/admin/v1/auth/login` 提交用户名与口令,或向 `/admin/v1/admins` 提交建号请求
- **THEN** 该路由不存在/不接受本地凭据,请求不产生任何会话

#### Scenario: 停用即时踢会话

- **WHEN** 主体 A 已持会话,另一管理员将 A 停用
- **THEN** A 的下一请求返回 401,存量会话不再可用,且 A 无法再次登录

#### Scenario: 注销立即失效

- **WHEN** 持会话管理员调用注销端点
- **THEN** 该会话立即失效,重放返回 401

#### Scenario: 零存量重建拒绝本地账密行

- **WHEN** 旧库 `admin_users` 仍含本地账密管理员行
- **THEN** 启动被拒绝并指名这些行,不得静默丢弃数据

#### Scenario: 新主体默认最小权

- **WHEN** 某联邦 sub 首次登录自动建档
- **THEN** 主体 scope=`TENANT`、绑定为空,除 `whoami`/`logout`/认证入口外全部管理路由 403,
  直至 GLOBAL actor 授予范围

### Requirement: 管理面鉴权

管理面鉴权必须 (MUST) 接受「虎符 federated 会话 cookie / Bearer 管理令牌」任一;现行
`MUNIU_ADMIN_TOKEN` 路径必须 (MUST) 原样保留(CLI/脚本契约零破坏);两条路径请求必须
(MUST) 均识别为具名 actor(令牌轨保留身份、会话轨 id_token 展示名)。鉴权失败必须 (MUST)
保持现行 401 信封。管理面挂载条件必须 (MUST) 为「管理令牌已配置或存在活跃主体或虎符联邦
已声明」;皆无时管理面整体不注册。

鉴权成功只确定主体身份;路由是否可达必须 (MUST) 由 scope 与路由分类决定(见
`admin-tenant-scope`):`TENANT` 主体访问 `global-only` 路由必须 (MUST) 403,
授权失败不得复用 401 信封。共享管理令牌轨必须 (MUST) 恒为 `GLOBAL`。

#### Scenario: 令牌轨零回归

- **WHEN** 以现行 Bearer 管理令牌调用任一既有管理端点
- **THEN** 行为与变更前一致,既有测试零改动通过

#### Scenario: 双轨并存

- **WHEN** 分别以虎符会话 cookie 与 Bearer 令牌调用管理端点
- **THEN** 两者均获准,审计分别归因为展示名与令牌身份

#### Scenario: 皆未配置整体关闭

- **WHEN** 管理令牌未配置且无活跃主体且联邦未声明
- **THEN** 管理面与控制台不注册

#### Scenario: 鉴权通过不等于全路由放行

- **WHEN** 已认证的 TENANT 主体访问 `global-only` 管理路由
- **THEN** 返回 403(不是 401),资源处理器零触达,审计记 denied

### Requirement: 管理动作审计

管理面全部变更动作(非 GET)必须 (MUST) 留痕审计行:actor(kind + 名字)、action(路由模式
规整)、目标标识、结局(状态码,401/403 记 denied)、脱敏 detail、request_id、时间。
`actor_kind` 新行值域为 `federated`/`token`/`unauthenticated`(`session`/`login` 仅为历史
行兼容保留)。审计 detail 必须 (MUST) 对口令/令牌类敏感字段脱敏;审计行必须 (MUST) 只增
不改;查询必须 (MUST) 经 `GET /admin/v1/audit` 游标分页。

scope 拒绝必须 (MUST) 留痕:任意方法(含 GET)的 403 记 denied 并与固定原因码一并落审计;
非拒绝的 GET 不新增审计行。上述扩展不得改变既有归因、脱敏、只增与分页语义。

#### Scenario: 主体归因

- **WHEN** 经虎符登录的管理员调用变更端点
- **THEN** 审计行 actor_kind=federated、actor=id_token 展示名、目标=subject 或资源标识

#### Scenario: 敏感字段脱敏

- **WHEN** 变更请求体含口令/令牌类字段
- **THEN** 审计 detail 中该字段以脱敏形态存储,明文不落审计

#### Scenario: 游标分页查询

- **WHEN** 以游标参数调用审计查询
- **THEN** 返回该页审计行与下一游标,按时间倒序稳定

#### Scenario: scope 拒绝可追责

- **WHEN** TENANT 主体对范围外租户发起读或写(含 GET 列表/查询)
- **THEN** 产生 denied 审计行,含 actor、路由模式、目标租户/key 标识与固定原因码,
  无敏感 detail
