# Design

## Context

协议面与调度面均已就绪:anthropic 出站桥接(工具多轮完整双向)、会话亲和(ADR-0020)、per-provider 出站代理(2026-11-03)、上游健康熔断(ADR-0019)。缺口纯在凭据形态:`KeyRef` 现为纯 `{Env}` 引用,`auth` 值域 `""|bearer`,密钥全走静态配置;OAuth token 会轮换,静态配置必然失步。边界裁决见 ADR-0021(账号池复用 keypool / 凭据落库 / 声明制 / 不内置指纹伪装 / 首切片 Claude / 计量零改动)。

## Goals / Non-Goals

Goals:OAuth 账号全链入库可用——存储、授权流(PKCE)、刷新、入池调度、出站头形态、管理面与控制台;调度零特例;零新增依赖。

Non-Goals:Codex/Gemini OAuth(后续切片,同配方复制)、客户端指纹伪装缺省(UA 覆写仅声明制)、账号级并发闸、多用户订阅分发(平台面,ADR-0005 边界)、集群形态下账号跨实例同步(随 ADR-0006 演进)、授权流的多租户化。

## Decisions

1. **账号引用在 keys 成员级,渠道内同构**:`keys: [{account: acc_1}, {account: acc_2}]`,`auth: oauth` 渠道的 keys 全部为账号引用、不与 env 引用混编——鉴权头形态是适配器级语义,渠道内分叉即 per-key 特例;订阅与按量并存的混编由路由候选层表达(同一 base_url 声明 oauth 渠道与 bearer 渠道,别名候选并存,优先级/权重/换道既有机制)。加载期校验:oauth 渠道禁 env 成员、非 oauth 渠道禁 account 成员、同渠道重复账号引用拒绝(同 ID 双池成员凭据同步失准)、账号存在性在装配层(resolver 有 store 视野)校验。
2. **包边界:config 不 import store**。账号凭据解析经装配层注入的 resolver 闭包(`cmd/muniu` 把 `store` 的账号访问器交给 keypool 快照装配),依赖方向与既有 config-source 抽象同构(拓扑快照先例)。
3. **池成员持引用 ID,凭据固化进池 + 刷新器原位替换**:快照装配时从账号存储解析 access token 固化为池成员值(与静态 key 同形态,调度面零特例);刷新成功经 `SyncAccount` 锁内原位替换成员值——零拓扑操作、零池重建即达成「刷新热生效」。刷新器每轮末回调池同步闭包,存储侧状态(凭据/可用性)对齐进池。
4. **刷新器单 goroutine 定扫**:固定节奏扫描(缺省 30s),过期前固定窗口(缺省 30 分钟)内触发;失败重探的退避节奏即扫描间隔本身,不新造退避参数。401/403 与 400+invalid_grant(RFC 6749 撤销语义)→ `auth_failed` 终态(不重试,重新授权恢复);网络/5xx/其余 4xx 零回写下轮重探。verifier/state 仅内存态,重启即弃(授权流重跑无妨,不落库)。
5. **PKCE 用 stdlib**(sha256 + base64url + crypto/rand),零自研密码学、零新依赖;回调端点挂 admin mux 且为管理鉴权的唯一豁免(浏览器重定向不带管理凭据,state 的不可预测 + 单次 + TTL 即持有者凭据),回调地址声明制(`oauth.callback_url`)。
6. **OAuth 全局配置段声明制**:`oauth.authorize_url/token_url/client_id/callback_url` 四字段成套声明,部分声明加载期拒绝;不内置缺省端点常量(端点/头形态真机实证前不声称准确,以 example yaml 样例承载 Claude 官方形态),手动贴 token 通道不依赖此段。
7. **出站头常量单点**:anthropic 适配器 auth 三态分支(x-api-key 缺省 / bearer / oauth = Bearer + 官方 beta 头常量);UA 不覆写。beta 头真机形态待凭据实证,单点声明便于修正。
8. **lane 收窄双层**:加载期对静态路由尽力拒(静态可查部分),请求期对候选链必拒(热替换/控制面拓扑下加载期不可全查)——契约以请求期为准,显式错误信封与 `bridge_tool_roundtrip_unsupported` 同风格,上游零调用。
9. **计量与欠费零改动**:不声明价目 → cost=0(pricing-management 既有语义);订阅用量上限 429 不命中欠费白名单(402/insufficient_quota 封闭),走既有 ClassRateLimited 冷却——负面测试钉住白名单不放宽。
10. **顺带根修(变更内消费)**:lane 身份标记(`laneKey`)为 gemini 入站声明身份,一处机制两消费点——oauth 收窄判定与会话缓存亲和范围修正。后者是既有缺陷根修:gemini 委托 chat 管道不携带任何身份,曾被误当 chat 参与亲和,违反 ADR-0020 对 gemini 的零触碰声明;本变更以 lane 标记修正亲和判定(回归测试钉住),ADR-0020 本体的语义面不受影响。

## Risks / Trade-offs

- [上游按客户端指纹校验拒收] → 头常量单点 + UA 声明制可配;真机实证挂外部条件,ADR-0021 明示保号风险运营者自担
- [refresh token 明文落库] → 无法哈希(需原文使用);缓解=DB 文件权限与部署边界,管理面与文档如实披露;state-backup 备份含敏感凭据的事实登记
- [回调需要可达地址] → callback_url 声明制;远程部署不可达时手动贴 token 通道兜底
- [真凭据缺失,头形态/端点形态未实证] → httptest 假上游 + fixture 先行,真机冒烟挂账(诚实边界,验收声明不越证据)
- [账号运行中被删] → 池收缩 + 管理面删除引用校验双保险(存储层拒绝有引用删除,运行态按不可用收缩)

## Migration Plan

纯增量:迁移 0019 建 `oauth_accounts`;既有配置零变化(不声明 oauth 即现状)。回滚=移除配置引用 + 删账号行(凭据可重建,重授权即恢复),drop 表无数据损失风险。

## Open Questions

- Claude OAuth 的 client_id 形态(官方公开 CLI client_id vs 运营者自建 OAuth app)——外部条件,真凭据到位时定;配置声明制已容纳两形态,不阻塞实现
- beta 头常量的真机准确值——同上,挂真机冒烟
- token 端点请求形态(现实现 JSON 体;RFC 6749 §2.1 惯例 form-encoded)——挂真机冒烟,实证后如需切换仅动 `Flow.tokenRequest` 单点
