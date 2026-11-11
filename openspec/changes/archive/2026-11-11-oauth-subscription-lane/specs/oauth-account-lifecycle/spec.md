# Spec Delta

## Purpose

OAuth 订阅账号全生命周期:把包月订阅账号(Claude Pro/Max 等)的 OAuth 凭据纳入账号池,对外提供与静态 API key 同形的标准 API。本能力拥有配置契约、凭据存储、授权流、刷新与失效终态、出站鉴权头形态与 lane 收窄、管理面;调度不变式归 keypool-scheduling。

## ADDED Requirements

### Requirement: 配置契约与值域单源

`providers[].auth` 值域必须 (MUST) 扩展为 `""|bearer|oauth`,非法取值在加载期拒绝,报错文案与合法值域同源渲染(单源,不得手抄第二份清单);`oauth` 仅 anthropic 适配器可声明,其他适配器声明 `oauth` 加载期拒绝。声明 `oauth` 的渠道 keys 成员必须 (MUST) 全部为账号引用(`account:`,渠道内同构,不与 env 引用混编——鉴权头形态是适配器级语义,渠道内分叉即特例;订阅与按量并存由路由候选层表达),任一成员含 env 引用即加载期拒绝;非 `oauth` 渠道不得 (MUST NOT) 声明账号引用成员(声明制,防误用)。`auth: ""|bearer` 既有语义零变化(向后兼容)。

#### Scenario: 非法 auth 值加载期拒绝
- **WHEN** 配置声明 `auth:apikey`(不在值域内)
- **THEN** 加载失败,报错文案列出全部合法值(含 oauth)

#### Scenario: oauth 渠道混入 env 引用拒绝
- **WHEN** `auth: oauth` 渠道的 keys 同时含 `account:` 与 `env:` 成员
- **THEN** 加载失败,报错指明 oauth 渠道 keys 须全部为账号引用

#### Scenario: 非 oauth 渠道声明账号引用拒绝
- **WHEN** `auth: bearer` 渠道的 keys 含 `account:` 成员
- **THEN** 加载失败,报错指明账号引用仅限 auth=oauth 渠道

#### Scenario: 非 anthropic 适配器声明 oauth 拒绝
- **WHEN** openai 适配器的 provider 声明 `auth: oauth`
- **THEN** 加载失败,报错指明 oauth 仅 anthropic 适配器支持

### Requirement: OAuth 实例段与共享校验

实例级 `oauth:` 段(authorize_url/token_url/client_id/callback_url)必须 (MUST) 成套声明,部分声明加载期拒绝;三 URL 须为 http(s) 绝对 URL 且无 fragment;声明 `oauth` 渠道而段不完整必须 (MUST) 加载期拒绝。该段为实例级声明,不随控制面拓扑下发、热更需重启。同渠道重复引用同一账号必须 (MUST) 拒绝(同 ID 双池成员使凭据同步只达首个,其余成员持陈旧凭据);跨渠道共享账号合法。auth/账号声明的全部值域判定必须 (MUST) 由 YAML 加载与控制面拓扑守门两源同源消费(共享校验函数),不得只在文件源生效。

#### Scenario: oauth 段部分声明拒绝
- **WHEN** `oauth:` 段仅声明 token_url 与 client_id
- **THEN** 加载失败,报错指明四字段必须成套声明

#### Scenario: 同渠道重复账号引用拒绝
- **WHEN** 同一 `auth: oauth` 渠道的 keys 两次引用 `account: acc_x`
- **THEN** 加载失败(与控制面拓扑替换拒绝),报错指明每账号至多一个成员

#### Scenario: 控制面拓扑载荷同源校验
- **WHEN** 控制面拓扑热替换载荷声明非法 auth 值或重复账号引用
- **THEN** 快照替换被拒绝、旧快照保留,判定与 YAML 加载同一套共享校验

### Requirement: 账号凭据存储与安全披露

账号凭据必须 (MUST) 落 SQLite(`oauth_accounts`:id/provider/状态/access_token/refresh_token/过期时刻/账号元数据),配置面仅持引用不持凭据。refresh token 无法哈希(需原文使用)必须 (MUST) 明文落库,且该事实必须在文档与管理面如实披露(缓解=DB 文件权限与部署边界);凭据值不得 (MUST NOT) 出现在任何日志输出。删除账号必须 (MUST) 拒绝仍有 provider 配置引用的删除(报错列出引用方),无引用方可删。

#### Scenario: 引用中账号删除被拒
- **WHEN** 管理面删除仍被某 provider `account:` 引用的账号
- **THEN** 删除拒绝,报错列出引用方 provider 名

#### Scenario: 凭据不泄漏
- **WHEN** 任意请求/刷新路径输出日志
- **THEN** access/refresh token 均以脱敏形态出现,无原文

### Requirement: 授权流

管理面必须 (MUST) 提供授权发起端点:生成上游 OAuth consent URL,携带 PKCE(S256,verifier 由加密随机源生成)与一次性 state(TTL 内单次有效);回调端点用授权码换 token 并落库,state 不匹配或重放必须 (MUST) 拒绝。回调端点豁免管理凭据(state 的不可预测性 + 单次弹出 + TTL 即回调的持有者凭据,标准 OAuth 回调姿势),其余账号端点不走豁免。token 端点成功响应缺 refresh_token 时必须 (MUST) 拒绝落库并以错误信封响应(空 RT 凭据入库后刷新链路失真)。手动粘贴 token 必须 (MUST) 作为调试通道保留(直接录入凭据对)。

#### Scenario: state 重放拒绝
- **WHEN** 同一 state 第二次到达回调端点
- **THEN** 拒绝,不落库任何凭据

#### Scenario: 授权完成入池
- **WHEN** 回调以合法授权码 + 匹配 state 换取 token 成功
- **THEN** 账号落库且状态为可用,池在下次快照解析时纳管该账号

#### Scenario: 回调响应缺 refresh_token 拒绝落库
- **WHEN** token 端点对回调换发响应 2xx 但无 refresh_token 字段
- **THEN** 返回错误信封,凭据不落库、账号不创建

### Requirement: 刷新生命周期

系统必须 (MUST) 在 access token 过期前主动刷新;刷新成功后池内后续请求必须 (MUST) 使用新 token,零重启、零拓扑热替换;刷新响应未携带 refresh_token 时必须 (MUST) 保留存量 refresh token(RFC 6749 §6)。刷新返回 401/403,或 400 且错误码为 `invalid_grant`(RFC 6749 凭据撤销语义)时,账号必须 (MUST) 进入 `auth_failed` 终态(需重新授权),不得无限重试;刷新失败(网络错误/5xx/其余 4xx)按固定扫描节奏下一轮重探,账号保持可调度直至 token 实际失效。刷新为亚秒级瞬态,不设独立「刷新中」状态。

#### Scenario: 过期前主动刷新
- **WHEN** access token 距过期进入刷新窗口
- **THEN** 刷新器换发新 token,池内凭据热替换,期间在途请求不受影响

#### Scenario: 刷新被拒进入终态
- **WHEN** 刷新请求返回 401/403,或 400 且错误码 invalid_grant
- **THEN** 账号置 `auth_failed` 终态,不再刷新,管理面可见;重新授权后恢复

#### Scenario: 刷新失败重探
- **WHEN** 刷新请求网络失败或上游返回 5xx(或非 invalid_grant 的 4xx)
- **THEN** 凭据零回写,账号保持 active 与可调度,下一扫描轮重探

#### Scenario: 重新授权恢复
- **WHEN** `auth_failed` 终态账号经授权流(或手动录入)回写新凭据对
- **THEN** 状态回 active 并清除终态原因,池同步后恢复调度

### Requirement: 账号入池与调度不变式

账号凭据必须 (MUST) 以 key 池成员形态参与调度:三态状态机/冷却/WRR/欠费分桶/熔断让位对账号与静态 key 一视同仁,不得 (MUST NOT) 出现账号特例分支。账号不存在或已删除时其渠道 key 必须按不可用收缩(池健康语义),不得使请求击穿到空凭据出站。

#### Scenario: 调度等价
- **WHEN** 全账号渠道与全静态 key 渠道在同一别名下作为候选并存(渠道内同构,混编由候选层表达)
- **THEN** 两者按候选优先级/权重与冷却状态统一调度,行为与双静态渠道无差异

#### Scenario: 账号删除后收缩
- **WHEN** 账号在运行中被删除且配置引用仍在
- **THEN** 该渠道对应 key 进入不可用,候选域收缩,不产生空凭据出站请求

### Requirement: 订阅限流不进欠费桶

上游订阅账号的用量上限错误(如 429 usage limit 类形态)必须 (MUST) 归限流冷却语义,不得 (MUST NOT) 进欠费分桶——欠费证据白名单保持封闭,不因账号凭据形态扩展而放宽。

#### Scenario: 用量上限走冷却
- **WHEN** 订阅账号上游返回用量上限类 429
- **THEN** 该账号按限流冷却指数退避,欠费桶零记录

### Requirement: 出站鉴权头形态与 lane 收窄

OAuth lane 出站必须 (MUST) 按声明发送上游官方 OAuth 文档要求的鉴权头(Bearer + 官方 beta 头),必须 (MUST NOT) 缺省覆写 User-Agent 或其他客户端指纹头(声明制可选覆写,不内置缺省)。仅 chat/messages 两条入站 lane 可达 OAuth 渠道;responses/gemini/images/embeddings lane 命中 OAuth 渠道时必须 (MUST) 返回显式错误信封,上游零调用。

#### Scenario: 头形态按声明
- **WHEN** chat 请求路由到 auth=oauth 渠道
- **THEN** 出站携带 Bearer(access token)与声明的官方 beta 头,UA 与静态 key 路径一致

#### Scenario: 非 chat lane 命中拒绝
- **WHEN** embeddings 请求的候选链含 auth=oauth 渠道
- **THEN** 返回显式错误信封(指明 lane 不支持 OAuth 凭据),上游零调用

### Requirement: 管理 API 与可见性

管理面必须 (MUST) 提供账号列表/创建(手动调试录入)/删除(引用校验)端点与授权发起/回调端点(账号详情由列表承担,不设独立详情端点);列表暴露账号状态——可用(`active`)/过期(行 active 但已过期的展示推导)/`auth_failed`(终态,附原因)与凭据脱敏形态。操作接入既有管理面审计与鉴权,不得 (MUST NOT) 绕过既有 admin 中间件(回调端点为唯一豁免,见授权流 requirement)。管理 API 契约必须 (MUST) 与 openapi/api-types 同步(漂移门禁)。

#### Scenario: 未鉴权拒绝
- **WHEN** 未携带管理凭据请求账号列表端点
- **THEN** 与其他 admin 端点同语义拒绝

#### Scenario: 状态可见
- **WHEN** 账号刷新被上游拒绝进入终态
- **THEN** 列表显示 `auth_failed` 与原因,指导重新授权

#### Scenario: 过期推导可见
- **WHEN** 账号行为 active 但凭据过期时刻已过
- **THEN** 列表按过期推导展示(待刷新语义),不改写存储行状态
