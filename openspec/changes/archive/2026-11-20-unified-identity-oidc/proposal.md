# 统一身份:木牛 console 可选联邦独立 IdP(镜像)

> 镜像统一身份契约源(独立 IdP 项目,HANDOFF 2026-11-20 第 8 条);孔明同名镜像(可选 provider)。协调登记见 `longzhong/HANDOFF.md`。

## Why

木牛 console 运营者(`admin_users`)是独立第三套人类身份(本地 argon2id + 自管会话 + 共享令牌),与统一身份体系无关联。身份权威定为**独立 IdP 项目**(可独立发布,形态对齐木牛;三仓只做 RP,插拔=配置)。本件把 console 登录联邦到 IdP,消除第三套密码存储与自研会话/锁定逻辑,审计归因统一到 IdP 实名。dev 实测 `admin_users` 0 行 → 迁移零负担。

木牛设计哲学「控制面失联数据面照跑」(`internal/auth/platform.go`)→ 联邦只覆盖管理面,**必须保留本地 break-glass 管理员 + `MUNIU_ADMIN_TOKEN` 降级**(CLI/脚本/metrics 依赖共享令牌)。

本项目遵循**解耦热插拔**:木牛必须能 standalone 独立发布(本地账密 + 共享令牌);统一身份 IdP 联邦是**可选**凭据轨——配置启用即 SSO 与实名审计,未启用时本地/令牌两轨与原行为完全一致。

## What Changes

- console 登录加 IdP OIDC 联邦凭据轨(授权码 + PKCE,复用 `internal/oauth` 既有 PKCE 范式);`resolveActor` 从双轨扩三轨(会话 cookie / Bearer 令牌 / IdP 联邦),联邦轨按配置可选启用。
- `admin_users` 加 `origin`/`external_subject` 列(只增迁移),联邦管理员映射 IdP ADMIN(平凡映射:木牛管理员单档权限 ≈ IdP ADMIN)。
- 审计 `actor_kind` 值域扩展(federated),IdP 实名归因。
- 前端 Login 页加「统一身份登录」按钮,保留本地账密/管理令牌模式。
- 保留本地 break-glass 管理员 + 共享令牌:IdP 不可达时仍可登录。
- 数据面 key、`oauth_accounts` 零触碰。

## Capabilities

### Modified Capabilities

- `admin-operators`:三轨鉴权、联邦登录与影子映射、审计归因扩展、控制面失联降级。

## Non-goals

- 不动 `oauth_accounts`(上游 Anthropic 订阅凭据池,`oauth-account-lifecycle` 域;明文落库纪律与身份哈希相反,误收编出事故)。
- 不动数据面 key(`api-key-lifecycle` 域,已经锦官 `api_key_bindings` 联邦,零改动)。
- 不删 break-glass 本地管理员 / 共享令牌(CI/CLI/metrics 依赖)。
- 不动 mk2- 平台 key(锦官签发器缺失另列,不混入)。
- 不另立 SSO 服务(IdP 是独立项目,木牛只做 RP)。

## 影响与确认

- 用户可见变化:console 登录加「统一身份登录」入口,本地模式保留。`admin_users` 0 存量,实损零。
- 审计 `actor_kind` CHECK 约束在 `0012_admin_operators.sql`,新迁移重建或新表(守只增纪律)。
