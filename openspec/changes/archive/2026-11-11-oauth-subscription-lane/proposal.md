# Proposal

## Why

对标 cc-switch / magpie / cli-proxy-api / new-api / sub2api(2026-11-11 考察,ADR-0021)确认:负载均衡与智能路由 muniu 已不弱于对标集,唯一真缺口是**订阅转 API**——OAuth 订阅账号(Claude Pro/Max 等)转标准 API,cli-proxy-api/sub2api 以此为核心能力。补齐后网关覆盖「按量 API key + 包月订阅账号」两类上游凭据形态;协议面(anthropic 出站桥接、会话亲和、出站代理)已全部就绪,缺的只是凭据形态。

## What Changes

- 新增 OAuth 账号域:账号凭据落 SQLite(迁移 0019 `oauth_accounts`);授权码 + PKCE 授权流(consent URL 生成 + 回调换 token)+ 手动贴 token 调试通道;后台刷新器(refresh 401/403 与 400+invalid_grant → `auth_failed` 终态,网络错误按固定节奏下轮重探)
- keypool 扩 DB 凭据来源:keys[] 增账号引用成员(`account:`),账号与静态 key 同池混编(账号池负载均衡的形态基础),复用三态状态机/冷却/WRR/欠费分桶,零平行子系统
- 配置契约:`providers[].auth` 值域扩 `oauth`(现 `""|bearer`,仅 anthropic 适配器消费),provider 以 `account:` 声明账号引用;值域单源校验,向后兼容
- 出站形态:anthropic 适配器按官方 OAuth 文档发 Bearer + beta 头;**不内置客户端指纹伪装**(UA 覆写不做缺省,保号风险运营者自担);仅 chat/messages 两条 lane 接入(responses/gemini/images/embeddings 不接入)
- 管理面:账号列表/创建(手动调试录入)/删除端点(详情由列表承担)+ 授权发起端点 + 控制台账号页(亮暗/三档响应式走查)
- 计量零改动:订阅账号不声明价目即 cost=0(pricing-management 既有「无价目记 0」语义),声明价目即按价目

## Capabilities

### New Capabilities

- `oauth-account-lifecycle`:OAuth 订阅账号全生命周期——配置契约(auth 值域 + account 引用)、凭据存储与安全披露(refresh token 明文落库)、授权流(PKCE/state)、刷新与失效终态、账号入池语义、出站鉴权头形态、lane 范围、管理 API 与控制台

### Modified Capabilities

- `keypool-scheduling`:新增「账号凭据来源」requirement——`account:` 引用从 oauth_accounts 解析入池;池调度不变式(三态/冷却/WRR/欠费分桶/熔断让位)对账号凭据与静态 key 一视同仁

## Impact

- `internal/store`:迁移 0019 + `oauth_accounts` 访问器(建/读/列表/更新 token/状态迁移/删除)
- `internal/config`:`KeyRef.Account`(成员级账号引用,与 Env 互斥)+ auth 值域校验扩展(单源)
- `internal/keypool`:KeyRef 账号引用形态 + 凭据解析
- `internal/adapter`(anthropic):OAuth 头形态声明分支
- 新 `internal/oauth`:授权流 + 刷新器(PKCE = stdlib sha256/base64,state = crypto/rand,零自研密码学,零新增依赖)
- `internal/admin` + `web`:账号端点与控制台页;openapi/api-types 同步(漂移门禁)
- 外部条件(ADR-0021):真实订阅凭据到位前真机冒烟挂账,fixture/单测/集成验证先行,不声称已验证
