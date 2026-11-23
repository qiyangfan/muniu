# 任务:管理面准入改为 Keycloak client role

## 1. 失败测试

- [x] 1.1 联邦回调:含 `muniu-admin` client role → 登录成功;缺角色 → 拒绝且不建主体
- [x] 1.2 realm role / 其它 client role / 缺 `resource_access` → 一律拒绝
- [x] 1.3 角色撤销后重新登录 → 拒绝;恢复后重新登录 → 成功
- [x] 1.4 配置:仅 issuer/client_id/redirect_url 即视为完整声明;`admin_subjects` 不再必需

## 2. 实现

- [x] 2.1 `oidc.Identity` 增加 `ClientRoles`,按 `client_id` 解析 resource_access
- [x] 2.2 `AdminService.LoginFederated` 改为角色白名单;删除 sub 白名单与 `allowed`
- [x] 2.3 删除 `admin_oidc.admin_subjects` 字段/校验/完整性条件;更新装配
- [x] 2.4 回调透传 roles;审计原因与文案更新

## 3. platform(跨仓)

- [x] 3.1 `setup-realm.sh` 建 `muniu-admin` client role、赋 demo、加 ID token 角色 mapper;幂等

## 4. 文档与验收

- [x] 4.1 `muniu.example.yaml`、`muniu.yaml`(本地)、`docs/runbook-keycloak-oidc.md` 同步
- [x] 4.2 Keycloak 运行时:demo 登录成功;无角色用户拒绝;撤销角色后重登拒绝;令牌轨回归
      实测:demo whoami kind=federated;demo-user 无角色 401;撤销角色后重登 401;
      恢复后成功;共享令牌轨 whoami kind=token 200
- [x] 4.3 `make check` 全绿 + `openspec validate --all --strict` 通过
      实测(2026-11-23,生成物入索引后):`make check` EXIT=0,含 web build/e2e/size、
      test 覆盖 75.0%、plugin 70.45%、strict 32/32。
