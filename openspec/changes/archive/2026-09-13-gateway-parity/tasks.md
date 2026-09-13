# 任务清单:网关标配对齐

> 纪律:测试先行域(auth 过期判定/指标契约)先红后绿;每个任务收尾 = 实现 + 测试 + `make check` 全绿 + 勾选。
> 兼容承诺:全部为加法,既有全量测试零改动通过是每个批次的回归闸门。

## 1. 契约前置

- [x] 1.1 `internal/protocol`:`TypeNotFound="not_found"` + `statusByType` 映射 404;负面测试钉「未知类型仍 500 兜底」不回归;conventions 错误类型表同步
- [x] 1.2 `contracts/openapi.yaml`:`GET /v1/models`、`GET /v1/models/{model}`、keys 签发请求/响应新字段、`PATCH /admin/v1/keys/{id}`、NotFound 信封 schema;api-types 重新生成(路由漂移门禁通过)

## 2. 模型发现(model-discovery)

- [x] 2.1 拓扑快照暴露别名目录(含快照加载时刻);`GET /v1/models` 与 `GET /v1/models/{model}` handler(读快照、id 升序、owned_by=muniu);路由挂载于鉴权之后、quota/ratelimit/计量/插件之外;单测:列表与快照同源、升序确定、未命中 404 not_found 信封、未鉴权 401
- [x] 2.2 端到端钉子:热替换后列表同步变化;配额耗尽 key 仍可列模型(诊断链路不被阻断)

## 3. key 生命周期(api-key-lifecycle)

- [x] 3.1 迁移 0006(`api_keys.expires_at` 可空 DATETIME)+ store 层:`GetAPIKeyByHash` 带出、`CreateAPIKey` 接收、`UpdateAPIKeyLimits` 局部更新;CRUD 单测(空值往返/null 清除/未知 key)
- [x] 3.2 auth:`KeyInfo.ExpiresAt`;verify 成功后单点过期判定(当前时钟,覆盖查库与缓存命中两路径);mk2 路径 `ExpiresAt` 置零不重复判;表驱动先红后绿:未过期放行/过期即拒(缓存命中路径钉死)/message 区分「已过期」/非法格式 400
- [x] 3.3 admin:`POST /admin/v1/keys` 暴露 `tpm_limit`/`daily_quota_credits`/`expires_at`;`PATCH /admin/v1/keys/{id}`(局部更新/null 清过期/写后 Invalidate/404/400 整单拒绝);全流程测试(签发→改额→即生效→清过期)
- [x] 3.4 CLI `keys` 子命令 flags(`--tpm`/`--daily-quota-micro`/`--expires-at` RFC3339);控制台 Keys 页:签发表单三字段 + 列表列 + 行内编辑(复用既有组件,组件库范式);openapi 同步与 api-types 一致(1.2 已备);控制台手测实录

## 4. 指标深度(usage-metrics)

- [x] 4.1 `muniu_tokens_total{direction,provider,model}`:计量落账点三向累加(prompt/cached_prompt/completion,口径=落账行含估算)
- [x] 4.2 `muniu_semantic_cache_queries_total{model,result=hit|miss|skip}`:缓存判定分支计数(先红后绿:三分支各钉一组)
- [x] 4.3 `muniu_first_token_duration_ms`(桶 1ms 起步):流式首帧观测、非流式零观测;契约测试:禁用=无新序列且行为零变化、基数=配置界
- [x] 4.4 README metrics 清单 + `docs/design/architecture.md` 端点/指标清单同步

## 5. 收尾

- [x] 5.1 文档:`docs/design/data-and-config.md` key 段(expires_at/PATCH)、`muniu.example.yaml` 注释样例如需;`docs/design/upstream-adapters.md` 或 conventions 中 `/v1` 端点清单补模型发现
- [x] 5.2 终验:`make check` 全绿(vet + lint 0 issues + web 门禁 + 全包 race);验收证据(命令+输出)贴 HANDOFF 验收记录;smoke 直连冒烟 `/v1/models` 实录
