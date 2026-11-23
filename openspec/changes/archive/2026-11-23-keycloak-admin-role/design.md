# 设计:client role 准入

## 结论

`AdminService.LoginFederated` 接收 RP 验签后的 `ClientRoles`,要求包含固定角色
`muniu-admin`;删除 sub 白名单。角色名是平台契约,硬编码在 RP 内,未知角色一律忽略。
决策记录见 `docs/adr/0024-admin-admission-client-role.md`。

## 备选比较

| 方案 | 结论 | 理由 |
| --- | --- | --- |
| 保留 sub 白名单 | 否决 | 身份授权留在产品配置,与平台裁决相反;无法承接 IdP 侧治理 |
| 白名单 OR 角色双轨 | 否决 | 两个权威,离职/撤销不收敛,审计口径分裂 |
| client role 白名单映射 | 采纳 | Keycloak 单一权威;本地只缓存;改动集中在回调与服务准入 |

## 角色来源

- claim:`resource_access["muniu-admin"].roles`;client_id 来自 `admin_oidc.client_id`。
- 仅 `muniu-admin` 授准入;`realm_access`、其它 client、未知角色全部忽略。
- 缺 claim / 空 roles → 拒绝;拒绝原因沿用 `subject_not_allowed_or_disabled` 审计码,
  文案改为「缺少管理员角色或已停用」。失败不建主体、不签发会话。

## 生效与失败模式

- 角色变更在用户下一次重新登录时收敛;本地会话仍按管理面 12h TTL(与现状一致)。
- 主体停用仍即时 401;启用不恢复旧会话。
- IdP 不可达只影响新登录;共享令牌轨与既有会话不受影响。
