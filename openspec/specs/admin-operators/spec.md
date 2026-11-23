# admin-operators Specification

## Purpose

管理面身份与审计契约:管理员主体(Keycloak sub 主键)生命周期、双轨鉴权(Keycloak会话/共享令牌)、管理动作审计归因与查询、Keycloak失联降级。历史来源:ADR-0017「共享令牌零归因 → 可追责」,本地账密轨已由 ADR-0022 删除,不破坏既有 Bearer token 契约。

## Requirements

### Requirement: 管理员主体与会话

管理员主体必须 (MUST) 以Keycloak sub 为唯一键(`admin_principals.subject` 主键);展示名来自
id_token `name`,必须 (MUST) 不参与身份判定(同名不同 sub 可并存)。系统必须 (MUST NOT)
接受任何本地口令登录,不得保存 `password_hash` 作为登录凭据。登录成功必须 (MUST) 签发
HttpOnly 会话 cookie(服务端只存令牌哈希,固定有效期),注销必须 (MUST) 立即失效该会话。
会话校验必须 (MUST) 逐请求确认主体仍活跃——主体停用后其全部存量会话必须 (MUST) 即时失效
(下一请求即 401),启用必须 (MUST NOT) 恢复旧会话。主体只能由Keycloak首登自动建档;管理必须
(MUST) 提供按 subject 的列表/停用/启用(管理 API 与 `muniu admins` CLI)。

#### Scenario: 登录签发会话

- **WHEN** 活跃主体经Keycloak OIDC 授权码 + PKCE 完成登录
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

### Requirement: 主体管理范围

主体必须 (MUST) 携带管理范围 `GLOBAL|TENANT`(范围模型与执行归 `admin-tenant-scope`),
新建主体必须 (MUST) 默认 `TENANT`;主体列表必须 (MUST) 回显 scope 与租户绑定。
范围的授予与变更仅 `GLOBAL` actor 可执行,且不改变身份键与停用语义。

#### Scenario: 新主体默认最小权

- **WHEN** 某联邦 sub 首次登录自动建档
- **THEN** 主体 scope=`TENANT`、绑定为空,除 `whoami`/`logout`/认证入口外全部管理路由 403,
  直至 GLOBAL actor 授予范围

### Requirement: 管理面鉴权

管理面鉴权必须 (MUST) 接受「Keycloak federated 会话 cookie / Bearer 管理令牌」任一;现行
`MUNIU_ADMIN_TOKEN` 路径必须 (MUST) 原样保留(CLI/脚本契约零破坏);两条路径请求必须
(MUST) 均识别为具名 actor(令牌轨保留身份、会话轨 id_token 展示名)。鉴权失败必须 (MUST)
保持现行 401 信封。管理面挂载条件必须 (MUST) 为「管理令牌已配置或存在活跃主体或Keycloak联邦
已声明」;皆无时管理面整体不注册。

鉴权成功只确定主体身份;路由是否可达必须 (MUST) 由 scope 与路由分类决定(见
`admin-tenant-scope`):`TENANT` 主体访问 `global-only` 路由必须 (MUST) 403,
授权失败不得复用 401 信封。共享管理令牌轨必须 (MUST) 恒为 `GLOBAL`。

#### Scenario: 令牌轨零回归

- **WHEN** 以现行 Bearer 管理令牌调用任一既有管理端点
- **THEN** 行为与变更前一致,既有测试零改动通过

#### Scenario: 双轨并存

- **WHEN** 分别以Keycloak会话 cookie 与 Bearer 令牌调用管理端点
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
`actor_kind` 值域为 `session`/`token`/`login`/`federated`(`session` 仅为历史行兼容保留;
`login` 用于登录事件与匿名动作行,见 ADR-0023)。审计 detail 必须 (MUST) 对口令/令牌类
敏感字段脱敏;审计行必须 (MUST) 只增不改;查询必须 (MUST) 经 `GET /admin/v1/audit` 游标分页。

scope 拒绝必须 (MUST) 留痕:任意方法(含 GET)的 403 记 denied 并与固定原因码一并落审计;
非拒绝的 GET 不新增审计行。上述扩展不得改变既有归因、脱敏、只增与分页语义。

#### Scenario: 主体归因

- **WHEN** 经Keycloak登录的管理员调用变更端点
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

### Requirement: 联邦登录与主体投影

console 必须 (MUST) 支持经Keycloak OIDC 授权码 + PKCE 登录;同一 sub 必须 (MUST) 恒投影为同一主体,不得生成影子 id;只有 id_token 的 `resource_access[admin_oidc.client_id].roles` 含 `muniu-admin` 的主体可以 (MUST) 建号并登录;必须 (MUST NOT) 以 sub 白名单、email 或 realm roles 作为准入依据。缺失 claim、仅含未知角色或其它 client 角色时拒绝;同时含 `muniu-admin` 时按放行处理。拒绝不得 (MUST NOT) 建主体或签发会话。登录页必须 (MUST) 只展示「统一身份登录」入口(联邦未声明时禁用并提示)与管理令牌入口。

#### Scenario: 统一身份登录

- **GIVEN** Keycloak id_token 的 `resource_access[admin_oidc.client_id].roles` 含 `muniu-admin`
- **WHEN** 运营者点「统一身份登录」并完成授权码 + PKCE
- **THEN** 建/取本地主体并签发会话,actor 归因为展示名

#### Scenario: 缺管理员角色被拒

- **GIVEN** 验签通过的 id_token 缺少 `muniu-admin` 角色
- **WHEN** 该主体完成上游认证回到回调
- **THEN** 拒绝建号登录并回控制台错误页,不签发会话

#### Scenario: realm role 忽略

- **GIVEN** 仅 `realm_access.roles` 或其它 client 的 roles 含 `muniu-admin`,本 client 不含
- **WHEN** 该主体完成上游认证回到回调
- **THEN** 拒绝

#### Scenario: 角色撤销后重新登录收敛

- **GIVEN** 某主体曾在有角色时登录成功
- **WHEN** Keycloak 撤销其 `muniu-admin` 角色后重新登录
- **THEN** 本次登录拒绝;恢复角色后重新登录成功

#### Scenario: 回调必须绑定发起浏览器

- **GIVEN** 发起授权的浏览器持有 state cookie
- **WHEN** 回调缺少或携带不匹配的 state cookie
- **THEN** 拒绝回调,state 不可复用

#### Scenario: 统一登出

- **WHEN** 已登录操作员退出
- **THEN** 删除本地会话并返回Keycloak end_session 地址;权威不可达时退回控制台

#### Scenario: 联邦登录留痕

- **WHEN** 回调完成(成功或拒绝)
- **THEN** 写 `auth.oidc.login` 审计,只含 provider 与固定原因码

#### Scenario: Secure cookie 由部署决定

- **GIVEN** 生产经反代终止 TLS
- **WHEN** `admin_cookie_secure: true`
- **THEN** 会话与 state cookie 均带 Secure,不依赖请求的 TLS 状态

#### Scenario: 登录入口按 IP 限流

- **GIVEN** 同一 IP 每分钟超过 30 次登录发起或回调
- **WHEN** 再次请求
- **THEN** 返回 429,不写 state、不触发出站发现

#### Scenario: 只发起不回调的 state 不堆积

- **GIVEN** 历史 state 已超过 TTL
- **WHEN** 新的登录发起写入 state
- **THEN** 过期行随本次写入被删除

#### Scenario: sub 稳定投影

- **GIVEN** sub 已建档
- **WHEN** 同一Keycloak用户再次登录
- **THEN** 命中既有主体,不重复建号

### Requirement: Keycloak失联降级

Keycloak权威不可达时,`MUNIU_ADMIN_TOKEN` 必须 (MUST) 仍可认证,数据面照跑;联邦轨失败必须 (MUST NOT) 阻断令牌轨。

#### Scenario: Keycloak不可达仍可用令牌

- **GIVEN** Keycloak权威不可达
- **WHEN** 以共享令牌调用管理端点
- **THEN** 认证成功,管理面可用
