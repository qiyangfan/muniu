# 木牛管理面接入 Keycloak(OIDC RP)runbook

> 配套 `../../docs/INTEGRATION-PLAN.md` §1 与 `../../platform/docs/spikes/2026-11-keycloak-light-theme.md`
> (相对本文件位于 `muniu/docs/`,跨仓文档在 longzhong 根)。
> 本仓只做标准 OIDC RP;IdP 为自托管 Keycloak(realm `longzhong`)。本地账密已删除,不恢复。

## 0 前置

- Keycloak + PostgreSQL 栈:`cd ../../platform && podman compose up -d`
  (VM 内存紧张,Keycloak 必须限堆,见 spike 决策记录"运行约束")。
- realm `longzhong`、client `muniu-admin`(confidential、PKCE S256、redirect 已登记)、
  演示用户:`bash ../../platform/scripts/setup-realm.sh`(幂等)。
- 取 client secret 与管理员 sub:

```bash
KC=http://127.0.0.1:8080
TOKEN=$(curl -sf -d client_id=admin-cli -d username=admin -d password=admin \
  -d grant_type=password "$KC/realms/master/protocol/openid-connect/token" | jq -r .access_token)
CID=$(curl -sf -H "Authorization: Bearer $TOKEN" "$KC/admin/realms/longzhong/clients?clientId=muniu-admin" | jq -r '.[0].id')
curl -sf -H "Authorization: Bearer $TOKEN" "$KC/admin/realms/longzhong/clients/$CID/client-secret" | jq -r .value
curl -sf -H "Authorization: Bearer $TOKEN" "$KC/admin/realms/longzhong/users?username=demo" | jq -r '.[0].id'
```

## 1 配置(`muniu.yaml`,gitignore,secret 走环境变量)

```yaml
admin_cookie_secure: false   # 本地 http;生产反代 TLS 显式 true
admin_oidc:
  issuer: "http://127.0.0.1:8080/realms/longzhong"   # 与发现文档逐字一致
  client_id: "muniu-admin"
  client_secret: "${MUNIU_KC_CLIENT_SECRET}"
  redirect_url: "http://127.0.0.1:8787/admin/v1/auth/oidc/callback"
  post_logout_redirect_url: "http://127.0.0.1:8787/console/"
  admin_subjects: ["<Keycloak sub>"]   # 管理面白名单,名单外一律拒绝
```

## 2 启动

```bash
export MUNIU_KC_CLIENT_SECRET=<client secret>
export MUNIU_ADMIN_TOKEN=<break-glass token>
make run   # 或 go build -o muniu ./cmd/muniu && ./muniu -c muniu.yaml
```

## 3 验收清单(对应 INTEGRATION-PLAN §1)

- [ ] `/console/` → 统一身份登录 → Keycloak 品牌登录页 → 回调进控制台;`whoami` 返回 `federated`
- [ ] JWKS 验签;Keycloak 轮换 kid 后仍可验
- [ ] 白名单外 sub 拒绝(负向)
- [ ] 禁用用户后会话收敛(负向)
- [ ] break-glass `MUNIU_ADMIN_TOKEN` 仍可用;联邦失败不阻断 token 轨
- [ ] 旧本地密码登录路由不存在(404/401)
- [ ] `/auth/logout/sso` → Keycloak `end_session` → 回 `/console/`

## 4 数据重置(greenfield,不迁移)

- `admin_principals`/`admin_sessions` 按新 Keycloak sub 重建;旧 hufu sub 行不迁移。
- 迁移 0021 升级时已清空 sessions;principals 旧行学习项目可直接清。

## 5 回滚

- 代码回滚 = 恢复上一提交;配置回滚 = 移除 `admin_oidc` 块(回到仅 token 运维轨)。
