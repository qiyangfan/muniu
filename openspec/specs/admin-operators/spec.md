# admin-operators Specification

## Purpose

管理面身份与审计契约:本地管理员账号生命周期、三轨鉴权(本地会话/共享令牌/锦官 OIDC 联邦)、登录硬化纪律、管理动作审计归因与查询、控制面失联降级。把「共享令牌零归因」的运维面升级为「可追责」,同时不破坏既有 Bearer token 契约。

## Requirements

### Requirement: 管理员账号与会话

系统必须 (MUST) 支持本地管理员账号(username 唯一,软停用不物理删除);账密登录成功必须 (MUST) 签发 HttpOnly 会话 cookie(服务端存令牌哈希,固定有效期),注销必须 (MUST) 立即失效该会话。会话校验必须 (MUST) 逐请求确认账号仍活跃——账号停用后其全部存量会话必须 (MUST) 即时失效(下一请求即 401);密码修改必须 (MUST) 注销该账号全部会话。管理员账号管理必须 (MUST) 经管理面与管理 CLI 双形态(创建/列表/停用启用/改密)。

#### Scenario: 登录签发会话

- **WHEN** 活跃管理员以正确账密调用登录端点
- **THEN** 返回会话 cookie(HttpOnly)与操作员身份,后续带 cookie 的管理面请求获准

#### Scenario: 停用即时踢会话

- **WHEN** 管理员 A 已持会话,另一管理员将 A 停用
- **THEN** A 的下一请求返回 401,存量会话不再可用

#### Scenario: 改密注销全部会话

- **WHEN** 管理员 A 的密码被修改
- **THEN** A 的全部存量会话失效,新密码登录后才恢复

#### Scenario: 注销立即失效

- **WHEN** 持会话管理员调用注销端点
- **THEN** 该会话立即失效,重放返回 401

### Requirement: 管理面三轨鉴权

管理面鉴权必须 (MUST) 接受「会话 cookie / Bearer 管理令牌 / 锦官 OIDC 联邦」任一;现行 `MUNIU_ADMIN_TOKEN` 路径必须 (MUST) 原样保留(CLI/脚本契约零破坏);三轨请求必须 (MUST) 均识别为具名 actor(令牌轨保留身份、会话轨 username、联邦轨锦官实名)。鉴权失败必须 (MUST) 保持现行 401 信封。管理面挂载条件必须 (MUST) 为「管理令牌已配置或存在活跃管理员账号或联邦已配置」;皆无时管理面整体不注册。

#### Scenario: 令牌轨零回归

- **WHEN** 以现行 Bearer 管理令牌调用任一既有管理端点
- **THEN** 行为与变更前一致,既有测试零改动通过

#### Scenario: 三轨并存

- **WHEN** 分别以会话 cookie、Bearer 令牌、锦官联邦调用管理端点
- **THEN** 三者均获准,审计分别归因到 username、令牌身份、锦官实名

#### Scenario: 皆未配置整体关闭

- **WHEN** 管理令牌未配置且无活跃管理员账号且联邦未配置
- **THEN** 管理面与控制台不注册

### Requirement: 登录硬化

口令必须 (MUST) 以 argon2id(PHC 串形态,参数随串存储可迁移)存储,校验必须 (MUST) 恒时比较;同一 username 连续登录失败达到阈值必须 (MUST) 锁定该账号一段时间(锁定期内正确账密也拒绝),锁定计数为进程内存态(重启清零,登记为边界)。登录成功与失败(含锁定拒绝)必须 (MUST) 均产生审计行。

#### Scenario: 错误口令拒绝

- **WHEN** 以错误口令登录
- **THEN** 返回 401,不签发会话,产生失败审计行

#### Scenario: 连续失败锁定

- **WHEN** 同一 username 连续失败达到阈值后以正确账密再登录
- **THEN** 锁定期内仍返回 401,产生锁定拒绝审计行;锁定期过后正确账密恢复登录

#### Scenario: 口令不落明文

- **WHEN** 读取管理员存储
- **THEN** 仅存在 argon2id PHC 串,任何端点/列表/日志不回显口令或其哈希

### Requirement: 管理动作审计

管理面全部变更动作(非 GET)必须 (MUST) 留痕审计行:actor(kind + 名字)、action(路由模式规整)、目标标识、结局(状态码)、脱敏 detail、request_id、时间。`actor_kind` 值域必须 (MUST) 扩展以容纳联邦身份(federated),联邦管理员必须 (MUST) 归因到锦官实名。审计 detail 必须 (MUST) 对口令/令牌类敏感字段脱敏;审计行必须 (MUST) 只增不改;查询必须 (MUST) 经 `GET /admin/v1/audit` 游标分页。

#### Scenario: 联邦管理员归因

- **WHEN** 经锦官联邦登录的管理员调用变更端点
- **THEN** 审计行 actor_kind=federated、actor=锦官实名

#### Scenario: 变更动作归因

- **WHEN** 管理员 A 经会话调用 key 停用端点
- **THEN** 产生审计行:actor=A(username 轨)、action=该路由规整名、目标=key 标识、结局=成功状态码

#### Scenario: 敏感字段脱敏

- **WHEN** 变更请求体含口令字段(如创建管理员)
- **THEN** 审计 detail 中该字段以脱敏形态存储,明文不落审计

#### Scenario: 游标分页查询

- **WHEN** 以游标参数调用审计查询
- **THEN** 返回该页审计行与下一游标,按时间倒序稳定

### Requirement: 联邦登录与影子映射

console 必须 (MUST) 支持经锦官 OIDC 授权码 + PKCE 登录;联邦管理员必须 (MUST) 映射锦官 ADMIN(平凡映射);`admin_users` 必须 (MUST) 以 `origin`/`external_subject` 列(只增)记录联邦来源与权威 sub;同一 sub 必须 (MUST) 恒映射同一本地管理员。

#### Scenario: 锦官登录

- **WHEN** 运营者经「锦官登录」完成授权码 + PKCE
- **THEN** 签发本地会话,actor 归因锦官实名

#### Scenario: sub 稳定映射

- **GIVEN** external_subject 已映射
- **WHEN** 同一锦官用户再次登录
- **THEN** 命中既有本地管理员,不重复建号

### Requirement: 控制面失联降级

锦官权威不可达时,本地 break-glass 管理员账密登录与 `MUNIU_ADMIN_TOKEN` 必须 (MUST) 仍可认证(控制面失联数据面照跑);联邦轨失败必须 (MUST NOT) 阻断本地/令牌轨。

#### Scenario: 锦官不可达仍可登录

- **GIVEN** 锦官权威不可达
- **WHEN** 以本地 break-glass 账密或共享令牌登录
- **THEN** 认证成功,管理面可用

#### Scenario: 联邦失败不阻断本地

- **WHEN** 联邦轨认证失败
- **THEN** 本地/令牌轨不受影响
