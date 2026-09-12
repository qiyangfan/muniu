# 网关标配对齐(模型发现 · key 生命周期 · 指标深度)

## Why

对标同类开源网关(one-api/new-api/LiteLLM)的能力面,本仓核心数据面(failover 语义、计量精度、流式性能、测试纪律)已领先,差距集中在三处「标配面」,且全部属于「暴露已有能力」而非新造机制:

1. **模型发现缺失**:入站业务端点仅 `POST /v1/chat/completions` 与 `/v1/embeddings`。`GET /v1/models` 是 OpenAI 兼容生态的事实标配——聊天前端、IDE 插件、各类 SDK 靠它完成模型目录发现与连通性探测,缺失即大量客户端无法初始化。
2. **key 签发粒度落后于自家执行能力**:`api_keys` 表的 `tpm_limit`/`daily_quota_micro` 列已在限流/配额链路真实生效(`Quota.exceeded` 先 key 级后租户级两道闸),但签发入口不暴露,成为「死配置」;`mk-` 查库 key 无过期时间(`mk2-` 平台 key 反而有 `expires_unix`),吊销之外缺少「到期自动失效」这一令牌管理标配语义;已签发 key 的限额调整只能删除重签(被迫换密钥)。
3. **`/metrics` 缺运维核心序列**:token 流向、语义缓存判定分布、流式首字节延迟均已有数据(usage_logs 字段、缓存判定分支、首帧时刻),但监控面看不到,可观测叙事不完整(Higress/LiteLLM 标配)。

纪律:不扩依赖、不加新机制;商业化运营功能(兑换码/支付/多用户)与重型可观测集成(OTel/Langfuse)明确不做(宪法非目标,见 design「明确不做」)。

## What Changes

- **模型发现**:新增 `GET /v1/models`(别名目录,读拓扑快照,与转发同源、热替换同步)与 `GET /v1/models/{model}`;数据面鉴权必须,但不消耗 RPM/TPM/配额、不落计量行、不接插件挂点;错误信封契约扩展 `not_found`(404)。
- **key 生命周期**:签发(CLI + admin POST)暴露 `tpm_limit`/`daily_quota_micro`;`api_keys` 增 `expires_at` 可空列(迁移 0006,空=永不过期,现状等价);过期判定收口在 verify 成功后的单点,以当前时钟逐请求比对(验证缓存天然不影响精确性);新增 `PATCH /admin/v1/keys/{id}` 局部更新限额与过期(写后 `Invalidate`);过期与无效/吊销同归 `auth_failed`(401)但 message 可区分。
- **指标深度**:`/metrics` 补三组序列——`muniu_tokens_total{direction,provider,model}`(计量落账点三向累加)、`muniu_semantic_cache_queries_total{model,result=hit|miss|skip}`(缓存判定分支)、`muniu_first_token_duration_ms`(直方图,仅流式首帧,桶 1ms 起步);指标契约测试同步扩展。

## Capabilities

### New Capabilities

- `model-discovery`: OpenAI 兼容模型发现端点的目录来源、鉴权与零计量语义、404 契约。
- `api-key-lifecycle`: `mk-` key 的签发粒度、过期判定语义、运行态限额调整契约。
- `usage-metrics`: `/metrics` token/缓存判定/首字节延迟序列的口径、基数界与禁用等价。

### Modified Capabilities

(无——`not_found` 错误类型的契约扩展在 `model-discovery` 内声明,并同步 conventions 错误类型表。)

## Impact

- 代码:`internal/protocol`(not_found)、`internal/auth`(ExpiresAt 过期判定)、`internal/store`(迁移 0006 + key CRUD 扩展)、`internal/admin`(keys POST/PATCH)、`cmd/muniu`(keys CLI flags)、`internal/proxy`(模型目录 handler、三组指标接线)、`web/src/pages/Keys.tsx`、`contracts/openapi.yaml` + 前端 api-types 重新生成。
- 文档:conventions 错误类型表、`docs/design/data-and-config.md`(key 段)、`docs/design/architecture.md`(端点与指标清单)、README 功能表。
- 风险面:纯加法;不配新字段、指标关闭时 standalone 行为零变化(既有测试零改动通过为闸门)。
