# 设计:木牛 console 可选联邦独立 IdP(镜像)

## 决策

- **可选联邦**:统一身份 IdP 联邦是第三凭据轨,按 `admin_oidc` 配置可选启用;未配置时本地账密 + 共享令牌两轨与原行为完全一致,木牛可 standalone 独立发布。联邦失败/不可达不改变本地两轨。
- **三轨鉴权**:`resolveActor`(`internal/admin/operators.go`)现双轨(会话 cookie 优先、Bearer 令牌次之),扩第三轨 IdP OIDC 联邦。三轨均识别具名 actor;令牌轨零回归(CLI/脚本契约)。
- **RP 客户端**:复用 `internal/oauth` 既有授权码 + PKCE 范式(现用于上游 Anthropic 订阅 lane),接独立 IdP;JWKS 验签。库选型(go-oidc/oauth2)起草时核 go.mod。
- **影子映射**:`admin_users` 加 `origin`(local|federated)/`external_subject`(IdP sub)列,只增迁移;联邦管理员平凡映射 IdP ADMIN。
- **审计扩展**:`actor_kind` CHECK(session|token|login)扩 federated;0012 约束需新迁移重建或新表(只增纪律)。
- **break-glass 降级(红线)**:IdP 不可达时本地账密 + 共享令牌仍可认证;联邦轨失败不阻断本地/令牌轨。这是木牛「控制面失联数据面照跑」哲学的管理面延伸。

## 排除(硬边界)

- `oauth_accounts` 是上游供应商凭据池(非人身份),与统一身份无关,不得触碰。
- 数据面 key 已经锦官 `api_key_bindings` 联邦,零改动。

## 待验证

- go-oidc/oauth2 依赖核实(起草时核 go.mod)。
- 生产/联调环境存量(本机 standalone dev,admin_users 0)。
- IdP 项目交付时间与 issuer/JWKS 契约(联调前置)。
