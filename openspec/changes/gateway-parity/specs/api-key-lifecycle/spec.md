## Purpose

`mk-` 查库 key 的签发粒度与生命周期:签发时限额字段完整可定(消除「存储与执行链路已在、入口不暴露」的死配置),支持到期自动失效,支持运行态限额调整而无需换密钥。零值/空值语义 = 现状等价(不限/永不过期),standalone 默认行为零回归。

## ADDED Requirements

### Requirement: 签发粒度完整

key 签发入口(CLI 与 `POST /admin/v1/keys`)必须 (MUST) 支持设定 `tpm_limit`、`daily_quota_micro`、`expires_at`;缺省(0/空)= 不限/永不过期(现状等价)。非零值必须 (MUST) 进入既有执行链真实生效:key 级 TPM 滑动窗口与 key 级日配额闸。

#### Scenario: 全字段签发即生效

- **WHEN** 以 `tpm_limit=100`、`daily_quota_micro=N` 签发 key 并以该 key 发起超限请求
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

`PATCH /admin/v1/keys/{id}` 必须 (MUST) 支持局部更新 `rpm`/`tpm_limit`/`daily_quota_micro`/`expires_at`:省略字段不动,`expires_at` 显式 null 清除过期;写后必须 (MUST) 立即失效鉴权缓存(下一请求即新限额);未知 key 返回 404 `not_found`,非法值 400 整单拒绝(不落库)。接口必须 (MUST) NOT 暴露 id/secret/tenant 等不可变或归属字段的改写。

#### Scenario: 改额下一请求生效

- **WHEN** 将某 key 的 `rpm` 从 10 改为 100 后立即以该 key 高频请求
- **THEN** 新限额即时生效(无重启、无等待缓存 TTL)

#### Scenario: 清除过期

- **WHEN** 对已设过期的 key PATCH `expires_at=null`
- **THEN** 该 key 恢复永不过期语义

### Requirement: 管理面可见性

key 列表(`GET /admin/v1/keys` 与控制台密钥页)必须 (MUST) 展示 `tpm_limit`、`daily_quota_micro`、`expires_at`;控制台签发表单提供对应输入(组件库范式),列表可见当前值。

#### Scenario: 列表回显

- **WHEN** 签发携带全部新字段的 key 后查询列表
- **THEN** 该条目回显三个字段的设定值(空值如实显示为「不限/永不过期」)
