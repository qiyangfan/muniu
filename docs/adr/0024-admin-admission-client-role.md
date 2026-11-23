# ADR-0024:管理面准入改为 Keycloak client role

- 状态:已接受(2026-11-23);取代 ADR-0023 第 1 条「sub 白名单」,ADR-0022 的 sub 主体投影、会话与审计结构不变。
- 背景:ADR-0023 把准入放在 `admin_oidc.admin_subjects`;身份授予留在产品配置,无法承接 IdP 侧的组织/SCIM 治理,且与平台「粗角色归 Keycloak」的目标相反。Keycloak client `muniu-admin` 已就绪。
- 上游依据:longzhong 根 ADR-0002(授权策略/IAM 目标);本仓 ADR-0022(主体投影)与 ADR-0023(其余联邦加固)。

## 决策

1. **准入**:联邦回调只读取 id_token 的 `resource_access[admin_oidc.client_id].roles`;必须含 `muniu-admin` 才建/取主体并签发会话。缺 claim、仅未知角色、仅其它 client 角色均拒绝;realm roles 与 email 不作为依据;拒绝不建主体、不签会话。
2. **配置**:删除 `admin_oidc.admin_subjects`;`issuer/client_id/redirect_url` 成套声明即启用。不做兼容别名。
3. **本地投影**:主体仍以 Keycloak sub 为唯一键;停用即时失效、启用不恢复旧会话;角色撤销在下一次登录收敛。
4. **兜底**:`MUNIU_ADMIN_TOKEN` 原样保留;IdP 不可达只影响新登录。

## 备选与否决

- 保留 sub 白名单:身份授权留在产品配置,撤销/离职不收敛;否决。
- 白名单 OR 角色双轨:两个权威,审计口径分裂;否决。
- realm role:跨 client 授权面泄漏;否决。

## 后果

- 破坏性配置变更;runbook、example 与 admin-operators spec 同步。审计 detail 的 provider 记为 keycloak。
- 回滚:恢复旧二进制与 `admin_subjects` 配置;主体表不清,按 sub 复用;Keycloak 侧角色映射须同步回退。
