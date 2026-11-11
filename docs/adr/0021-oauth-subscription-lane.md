# ADR-0021:OAuth 订阅账号 lane——订阅转 API 的边界与形态

- 状态:已接受(2026-11-11,对标 cc-switch / cli-proxy-api / sub2api 后立项)
- 关联:ADR-0003(适配器分类)、ADR-0004(配置/存储边界)、ADR-0013(Anthropic Messages 入站)、ADR-0020(会话亲和)

## 决策

muniu 增加 **OAuth 订阅账号 lane**:将 Claude Pro/Max 等订阅账号(OAuth 凭据)纳入账号池,经既有出站协议面对外提供标准 API。这是对标竞品后确认的唯一真缺口——负载均衡/智能路由 muniu 已不弱于对标对象,缺的是凭据形态。

边界五条(全部契约级):

1. **账号池沿用 keypool**:OAuth 账号 = 带生命周期的 key,入既有 key 池复用三态状态机/冷却/WRR/欠费分桶;不另造平行账号子系统。
2. **凭据落库**:OAuth token 会轮换,静态配置(yaml/env)必然失步——账号凭据落 SQLite(迁移 0019 `oauth_accounts`),配置面仅声明引用(`account:`);KeyRef 扩 DB 引用形态。
3. **声明制延续**:`providers[].auth` 值域扩 `oauth`(现 `""|bearer`);按上游公开 OAuth 文档要求的头形态发(Bearer + 官方 beta 头),**不内置客户端指纹伪装**(UA 覆写不做缺省),保号风险由运营者自担,如实登记。
4. **计量语义**:订阅账号包月不限量,默认按 0 成本落账(既有 cost=0 先例),声明价目即按价目;逐 attempt 落账、熔断、亲和全部复用,零特例。
5. **首切片收敛 Claude**:Codex(ChatGPT OAuth)/ Gemini(Code Assist OAuth)协议形态各异且外部条件不同,登记后续切片,不捆绑立项。

授权流程走管理面:授权码 + PKCE(state 防 CSRF),consent URL 生成 + 回调换 token 落库;手动粘贴 token 保留为调试通道。

## 理由

- completeness IS the product(2026-10-13 重裁框架):订阅转 API 是求职作品集与网关完整度的真实缺口,五家对标中 cli-proxy-api/sub2api 以此为核心能力。
- keypool 已实现账号池的全部调度语义,账号生命周期(refresh/失效/重授权)是其超集而非异构物——复用一行架构,平行子系统否决。
- ADR-0020 会话亲和与账号粘性天然协同:同会话粘同账号是上游风控友好形态,零新增机制。
- 出站代理(outbound-proxy-per-provider,2026-11-03)恰好补齐国际 OAuth 端点的网络可达性:OAuth 渠道天然可挂 per-provider 代理。

## 备选与否决

- **独立 OAuth 子系统(绕开 keypool)**:冷却/WRR/欠费分桶/熔断全部逻辑重复实现——否决。
- **token 进 yaml/env**:token 随刷新轮换,静态配置必然失步——否决(ADR-0004 存储边界自然延伸到凭据)。
- **内置 CLI 指纹伪装(UA/beta 头缺省覆写)**:对抗上游反滥用是持续军备竞赛,与保号立场冲突;仅声明制可选配置,不做内置——否决缺省形态。
- **一次立项三家(Claude/Codex/Gemini)**:外部条件(凭据/网络)互不共享,首切片收敛后按同配方逐家复制——否决捆绑。
- **只做手动贴 token、不做授权流**:账号进不来,功能不可用——否决(手动通道保留为调试面)。

## 后果

- 新 capability spec `oauth-account-lifecycle`;`keypool-scheduling`(DB 凭据来源)、`adapter-taxonomy`(auth 值域)delta。
- **诚实边界**:真实订阅凭据到位前,真机冒烟挂外部条件(fixture/单测/集成验证先行,不声称已验证)。三处外部形态实证前不声称准确:出站 beta 头常量值、token 端点请求形态(现实现为 JSON 体,RFC 6749 惯例为 form-encoded,以真机实测为准)、client_id 形态(官方公开 CLI client_id vs 自建 OAuth app)。
- 刷新失败语义:refresh 401/403 与 400+`invalid_grant`(RFC 6749 撤销语义)→ 账号 `auth_failed` 终态(需重新授权),不无限重试;网络错误/5xx/其余 4xx 零回写,按固定扫描节奏下轮重探(账号保持可调度直至 token 实际失效)。
- refresh token 明文落库(SQLite):现状密钥存哈希、OAuth token 无法哈希(需原文使用),缓解=DB 文件权限与部署边界;state-backup 备份含敏感凭据的事实登记。
- 与既有 lanes 的关系:仅 chat/messages 两条 lane 接入(与 ADR-0020 同范围);responses/gemini/images/embeddings 不接入。
