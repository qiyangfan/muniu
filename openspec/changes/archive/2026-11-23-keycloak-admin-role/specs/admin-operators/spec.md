# Spec Delta

## MODIFIED Requirements

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
