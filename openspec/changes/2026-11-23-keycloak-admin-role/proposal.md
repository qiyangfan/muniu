# 提案:管理面准入改为 Keycloak client role

## Why

- 现行准入是 `admin_oidc.admin_subjects` 的 sub 白名单:身份授予散落在木牛配置里,
  与平台「粗角色归 Keycloak」的裁决(longzhong 根 ADR-0002/IAM-TARGET)相反,也无法承接组织/SCIM
  后续治理。
- Keycloak client `muniu-admin` 已就绪;把「是否管理员」放到 client role
  `muniu-admin`,木牛只做 client role 校验与本地缓存,身份权威单一。

## What Changes

- 联邦回调解析 id_token 的 `resource_access["muniu-admin"].roles`,必须包含
  `muniu-admin` 才允许建/取主体并签发会话;缺失 claim、缺失本 client 角色(仅含未知角色
  或其它 client 角色)时拒绝;realm roles 不作为准入依据。
- 移除 `admin_oidc.admin_subjects` 配置、校验与完整性条件;不保留兼容别名。
- 本地 `admin_principals` 仍是授权/停用/审计的本地投影;停用语义与即时踢会话不变。
- platform `setup-realm.sh` 增建 client role 并赋 demo,增加仅限本 client 的 ID token
  角色 mapper(跨仓任务,已先行落地)。

## Non-goals

- 不改共享令牌轨(`MUNIU_ADMIN_TOKEN`)与 break-glass;不做多角色/细粒度权限。
- 不消费租户/邮箱等其它 claim;不做组织同步/SCIM。
- 不保留白名单兜底(双轨会制造两个权威)。

## Impact

- 管理面准入从「按 sub 名单」变为「按 client role」;角色撤销在**下一次重新登录**时生效。
- 配置破坏性变更:`admin_subjects` 删除;`muniu.yaml`/示例/runbook 同步。
- 安全:缺少本 client 管理员角色时静默拒绝并审计;不建主体;无 schema 变更。
