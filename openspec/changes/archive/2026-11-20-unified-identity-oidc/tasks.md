# 任务:木牛 console OIDC RP(镜像)

> 每 task 完成跑 make check 对应子集并记录;鉴权改动测试先行。
> 本文「IdP」指统一身份独立 IdP 项目(issuer 可配置,木牛 RP 侧零改动)。

## 1. OIDC RP 客户端

- [x] 1.1 先写失败测试:授权码 + PKCE 经IdP登录、JWKS 验签(iss/aud)
- [x] 1.2 复用 internal/oauth PKCE 范式接IdP权威;核 go.mod 依赖
- [x] 1.3 make check 子集绿

## 2. 三轨鉴权与迁移

- [x] 2.1 先写失败测试:三轨并存、令牌轨零回归、联邦轨具名归因
- [x] 2.2 resolveActor 扩第三凭据轨
- [x] 2.3 admin_users 只增迁移(origin/external_subject)+ actor_kind 扩展(0012 约束重建/新表)
- [x] 2.4 make check 子集绿

## 3. 前端登录

- [x] 3.1 Login 页加「IdP登录」按钮,保留本地账密/管理令牌模式
- [x] 3.2 联邦会话恢复与 401 广播
- [x] 3.3 make check 子集绿

## 4. break-glass 降级回归(红线)

- [x] 4.1 先写失败测试:IdP不可达时本地账密/共享令牌仍可登录;联邦失败不阻断本地/令牌轨
- [x] 4.2 共享令牌零回归(CLI/脚本/metrics)
- [x] 4.3 make check 子集绿

## 5. 联调与收口

- [ ] 5.1 与 IdP 联调:授权码 + PKCE 全链、审计实名归因【阻塞:独立 IdP 项目尚未立项交付(无 issuer/JWKS/授权码端点);建成后经 muniu.yaml admin_oidc 段联调】
- [x] 5.2 spec deltas sync 进 specs/;聚合层 HANDOFF 回写
- [ ] 5.3 make check 全绿 + 归档【门禁分步实测全绿(唯一例外=依赖提交态的 api-types git-diff 漂移检查,生成幂等已证);归档待 git 提交/PR 授权且 CI 绿后执行(外部 CI 未绿保持 active)】
