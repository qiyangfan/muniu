# api-key-lifecycle Specification

## Purpose
`mk-` 查库 key 的签发粒度与生命周期:签发时限额字段完整可定(消除「存储与执行链路已在、入口不暴露」的死配置),支持到期自动失效,支持运行态限额调整而无需换密钥。零值/空值语义 = 现状等价(不限/永不过期),standalone 默认行为零回归。

## Requirements

### Requirement: 签发粒度完整

key 签发入口(CLI 与 `POST /admin/v1/keys`)必须 (MUST) 支持设定 `tpm_limit`、`daily_quota_credits`(抽象额度单位 credit,1 credit = 本位币十亿分之一,大整数自然表达如 820 亿)、`expires_at`;缺省(0/空)= 不限/永不过期(现状等价)。非零值必须 (MUST) 进入既有执行链真实生效:key 级 TPM 滑动窗口与 key 级日配额闸。

#### Scenario: 全字段签发即生效

- **WHEN** 以 `tpm_limit=100`、`daily_quota_credits=N` 签发 key 并以该 key 发起超限请求
- **THEN** TPM 超限返回 429 `rate_limited`,日配额超限返回 402 `quota_exceeded`(key 级闸先于租户级)

#### Scenario: 缺省签发零回归

- **WHEN** 仅提供 tenant/name/rpm 签发(不传新字段)
- **THEN** key 行为与现状完全一致(tpm/配额不限,永不过期),既有测试零改动通过

### Requirement: 过期自动失效

`expires_at` 非空且当前时刻晚于该时刻时,该 key 必须 (MUST) 被拒绝:401 `auth_failed`,message 区分为「已过期」(与无效/吊销的可读文案可区分)。判定必须 (MUST) 以当前时钟逐请求执行,鉴权缓存路径同样生效——过期生效时刻不得因缓存而漂移(缓存只允许影响 DB 变更可见性,不影响时间函数精确性)。

#### Scenario: 过期即拒(含缓存命中路径)

- **WHEN** key 的 `expires_at` 已过,且该 key 此前 10s 内已被鉴权缓存命中过
- **THEN** 新请求返回 401,message 含「已过期」语义

#### Scenario: 未过期正常放行

- **WHEN** key 的 `expires_at` 在未来
- **THEN** 鉴权放行,行为与无过期 key 一致

#### Scenario: 时间格式校验

- **WHEN** 签发/更新携带非法 `expires_at`(非 RFC3339)
- **THEN** 请求 400 整单拒绝,不落库;合法 RFC3339(含过去时刻)接受

### Requirement: 运行态限额调整

`PATCH /admin/v1/keys/{id}` 必须 (MUST) 支持局部更新 `rpm`/`tpm_limit`/`daily_quota_credits`/`expires_at`:省略字段不动,`expires_at` 显式 null 清除过期;写后必须 (MUST) 立即失效鉴权缓存(下一请求即新限额);未知 key 返回 404 `not_found`,非法值 400 整单拒绝(不落库)。接口必须 (MUST) NOT 暴露 id/secret/tenant 等不可变或归属字段的改写。

#### Scenario: 改额下一请求生效

- **WHEN** 将某 key 的 `rpm` 从 10 改为 100 后立即以该 key 高频请求
- **THEN** 新限额即时生效(无重启、无等待缓存 TTL)

#### Scenario: 清除过期

- **WHEN** 对已设过期的 key PATCH `expires_at=null`
- **THEN** 该 key 恢复永不过期语义

### Requirement: 管理面可见性

key 列表（`GET /admin/v1/keys` 与控制台密钥页）MUST 展示 `tpm_limit`、`daily_quota_credits`（抽象额度单位 credit，1 credit = 本位币十亿分之一）、`expires_at`；控制台签发表单 MUST 提供对应输入，列表可见当前值。管理面 MUST 明确区分 Key ID、secret 与 prefix，并在列表和签发结果中提供真实 Key ID 的展示与复制。创建响应契约 MUST 如实声明运行时已经返回的 `key`、`id`、`prefix`、`tenant`，不得因本变更改名既有响应字段。

#### Scenario: 列表回显

- **WHEN** 签发携带全部新字段的 key 后查询列表
- **THEN** 该条目回显三个字段的设定值，空值如实显示为“不限/永不过期”

#### Scenario: 复制真实 Key ID

- **GIVEN** 列表或签发结果包含真实 Key ID
- **WHEN** 用户执行“复制 Key ID”
- **THEN** 剪贴板获得完整原始 `id`，与用量 `key_id` 一致，不是 secret 或 prefix，不转换大小写

#### Scenario: 创建响应声明与实际输出一致

- **WHEN** 用户成功签发新 Key
- **THEN** 响应及契约包含 `key`、`id`、`prefix`、`tenant`，客户端从该响应提供 ID 复制入口，secret 仍遵守既有一次性展示纪律

#### Scenario: 自定义 ID 可复制

- **GIVEN** 平台提供的 Key 使用合法自定义字符串 ID
- **WHEN** 用户查看并复制 Key ID
- **THEN** 系统按真实值显示和复制，不因 ID 不是 UUID 或具有某种前缀而替换、截短或拒绝它

#### Scenario: 缺失 ID 不伪造值

- **GIVEN** 兼容列表条目没有可用的 `id`
- **WHEN** 用户查看条目
- **THEN** 界面明确显示 ID 不可用并禁用复制，不以 prefix、secret 或序号充当 ID

### Requirement: 运行态启停(可逆禁用)

key 必须 (MUST) 支持可逆停用与恢复:管理面经 `PATCH /admin/v1/keys/{id}` 的 `disabled` 字段(true=停用/false=恢复,省略=不动),CLI 经 `keys disable <prefix>` / `keys enable <prefix>`。停用 key 的数据面请求必须 (MUST) 被拒绝:401 `auth_failed`,message 与无效/吊销/过期可区分(含「已停用」语义)。停用/恢复写后必须 (MUST) 立即失效鉴权缓存(下一请求即生效,不等缓存 TTL)。停用必须 (MUST) NOT 改变吊销终态语义:已吊销 key 不得经恢复操作复活。key 列表(管理 API 与控制台)必须 (MUST) 回显停用状态。

#### Scenario: 停用即时生效(含缓存命中路径)

- **WHEN** 停用一把此前 10s 内已被鉴权缓存命中过的 key,随后以该 key 发起请求
- **THEN** 返回 401 `auth_failed`,message 含「已停用」语义,不等缓存 TTL

#### Scenario: 恢复放行且其余字段无损

- **WHEN** 对已停用 key 执行恢复(PATCH `disabled=false` / CLI enable)
- **THEN** 请求恢复放行,该 key 的限额、过期等其余字段保持原值不变

#### Scenario: 吊销终态不可复活

- **WHEN** 对已吊销 key 执行恢复操作
- **THEN** 该 key 仍被 401 拒绝(吊销语义),不得恢复为可用

#### Scenario: 缺省零回归

- **WHEN** PATCH 请求不携带 `disabled` 字段
- **THEN** 行为与本变更前逐字节一致(既有测试零改动通过)

#### Scenario: 列表回显停用状态

- **WHEN** 停用一把 key 后查询 `GET /admin/v1/keys` 与控制台密钥页
- **THEN** 该条目如实展示停用状态,与启用/吊销可区分

### Requirement: 签发粒度扩展——模型白名单字段

key 签发入口（CLI `muniu keys create` 与 `POST /admin/v1/keys`）必须 (MUST) 支持设定 `allowed_models`（模型别名数组）：缺省/空数组 = 不限（现状等价，零回归）。白名单按**模型别名**（客户端可见的路由键）约束，不按上游模型名。条目必须 (MUST) 非空（经 trim 判定），含空条目的签发请求 400 整单拒绝不落库；重复条目必须 (MUST) 去重后落库。签发响应必须 (MUST) 回显 `allowed_models`。白名单值**不校验别名存在性**——拓扑热替换下允许先于别名签发（预配置语义）。

#### Scenario: 缺省签发零回归

- **WHEN** 仅提供 tenant/name/rpm 签发（不传 `allowed_models`）
- **THEN** key 行为与现状完全一致（全模型别名可用），既有测试零改动通过

#### Scenario: 带白名单签发即回显

- **WHEN** 以 `allowed_models=["m-flash","m-pro"]` 签发 key
- **THEN** 签发响应（含 `key`/`id`/`prefix` 等既有字段）携带 `allowed_models` 回显同样两别名，落库持久

#### Scenario: 空条目整单拒绝

- **WHEN** 以 `allowed_models=["m-flash","  "]` 签发
- **THEN** 返回 400 整单拒绝，不产生 key 行

#### Scenario: 预配置先于别名签发

- **WHEN** 以含拓扑中尚不存在的别名 `future-model` 的白名单签发
- **THEN** 签发成功（不因别名不存在而拒绝）

### Requirement: 数据面模型访问执行

鉴权成功的请求必须 (MUST) 受 key 白名单约束：模型别名解析成功后、语义缓存检索与任何上游调用**之前**执行校验；白名单外的别名必须 (MUST) 返回 403，错误信封 `error.type=forbidden`、`error.code=model_not_allowed`，上游零调用、语义缓存零回放（被禁请求不得吃缓存命中）、零计量落账。校验必须 (MUST) 覆盖全部入站 lane：chat、anthropic messages、responses、gemini（四 lane 的共享转发路径单点生效）、embeddings 与 images；插件钩子改写请求体中 `model` 字段后的重解析路径必须 (MUST) 再次校验（改写不得绕过白名单）。白名单为空（不限）时必须 (MUST) 恒放行，行为与现状逐字节一致。

#### Scenario: 白名单外拒绝且上游零调用

- **WHEN** 白名单 `["m-flash"]` 的 key 请求别名 `m-pro`（存在于拓扑）
- **THEN** 返回 403 信封（`type=forbidden`、`code=model_not_allowed`、含 request_id），上游无任何请求、无 usage_logs 行、语义缓存无检索副作用

#### Scenario: 白名单内正常放行

- **WHEN** 白名单 `["m-flash"]` 的 key 请求别名 `m-flash`
- **THEN** 转发全链与无白名单 key 行为一致（语义缓存/计量/治理均正常）

#### Scenario: 白名单外不得吃缓存命中

- **WHEN** 别名 `m-flash` 存在语义缓存命中条目，白名单 `["m-pro"]` 的 key 请求 `m-flash`
- **THEN** 返回 403 `model_not_allowed`，缓存条目不被回放

#### Scenario: 插件改写 model 后再校验

- **WHEN** 插件钩子把请求体 `model` 从白名单内的 `m-flash` 改写为白名单外的 `m-pro`
- **THEN** 重解析后校验仍以改写后的别名为准，返回 403 `model_not_allowed`

#### Scenario: 六 lane 同规

- **WHEN** 白名单 `["m-flash"]` 的 key 分别经 `/v1/messages`、`/v1/responses`、`/v1beta/models/{model}:generateContent`、embeddings、images 入站请求白名单外别名
- **THEN** 五条非 chat lane 均返回 403 访问拒绝且上游零调用（信封按各 lane 既有原生形态：协议信封携带 `type=forbidden`+`code=model_not_allowed`；Gemini 管道信封按 ADR-0015 改写语义映射 `PERMISSION_DENIED`；messages 流式按 Anthropic 词汇表映射 `permission_error`；chat 主路径另有专项场景）

#### Scenario: 空白名单恒放行

- **WHEN** 无白名单 key 请求任意已存在别名
- **THEN** 行为与本变更前完全一致（既有全量测试零改动回放）

### Requirement: 白名单管理面维护

`PATCH /admin/v1/keys/{id}` 必须 (MUST) 支持局部更新 `allowed_models`，三态语义：字段省略 = 不动；显式 `null` = 清除白名单恢复不限；数组 = 整体设定。写后必须 (MUST) 立即失效鉴权缓存（与限额 PATCH 同款协议，下一请求即新白名单）。key 列表（`GET /admin/v1/keys` 与控制台密钥页）必须 (MUST) 回显 `allowed_models`；控制台签发表单与行内编辑必须 (MUST) 提供输入。未知 key 404、非法载荷 400 整单拒绝不落库。`PATCH` 不得以部分数组语义合并（设定即整体替换）。

#### Scenario: PATCH 三态

- **WHEN** 对带白名单 key 分别 PATCH `{"allowed_models":["m-pro"]}` / `{"allowed_models":null}` / 省略该字段
- **THEN** 依次为：白名单变为 `["m-pro"]`；白名单清空恢复不限；白名单保持不变

#### Scenario: 改白名单下一请求生效

- **WHEN** PATCH 将 key 白名单加入别名 `m-pro` 后，立即以该 key 请求 `m-pro`
- **THEN** 请求正常转发（新白名单即时生效，不等鉴权缓存 TTL）

#### Scenario: 未知 key 与非法载荷

- **WHEN** 对不存在的 key ID PATCH；或 PATCH `allowed_models=[""]`
- **THEN** 前者 404 `key_not_found`；后者 400 整单拒绝，既有白名单不变
