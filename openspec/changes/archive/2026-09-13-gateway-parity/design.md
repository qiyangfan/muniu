# 设计:网关标配对齐

> 站在架构师视角的取舍记录;每条决策给出「为什么这样,为什么不那样」。

## 1. 模型发现

### 1.1 数据源:拓扑快照,与转发同源

`GET /v1/models` 的目录 = 拓扑快照 `model_routes` 的别名键集。理由:客户端拿到的模型名必须与转发实际可路由的别名严格一致,否则「列表里有、转发时 adapter_missing」比列表缺失更糟;读同一快照(请求入口 Load 一次)使目录与路由视图天然同步,热替换后列表即时变化,零额外一致性机制。

- 备选「admin API 另维护一份对外目录」:否——两源必然漂移,且运营负担无谓。
- 备选「包含 `models` 治理目录(能力声明)」:否——那是参数治理的内部契约(ADR-0010),不是客户端消费面;客户端可见的只有别名。

### 1.2 形态字段:诚实优先

`{id, object:"model", created, owned_by}` 对齐 OpenAI 形态。`owned_by` 恒为 `"muniu"`——别名是多候选换道语义,标注单一 provider 归属是失真信息;`created` 取快照加载时刻(进程内稳定,重启与热替换后更新),供按时间排序的客户端使用,不承诺「模型创建时间」语义。

排序按 `id` 升序:输出确定(测试可钉),与 admin pricing 列表惯例一致。

### 1.3 鉴权与链路位置:auth 后、quota/ratelimit 外

数据面鉴权必须(`/v1` 消费者功能,不匿名暴露目录);但**绕过配额闸、RPM/TPM 与计量**:①模型列表是元数据读,零上游成本,计入配额违背「配额闸拦上游成本」的本义;②配额耗尽/被限流的客户端恰恰需要列模型来诊断——此时若 402/429,诊断链路自断;③不落计量行(非 LLM 调用,落行即污染对账口径);④不接插件挂点(挂点契约是转发管道,模型目录非流量,且插件 ABI 未定义只读元数据钩子)。

### 1.4 404 契约:`not_found` 类型扩展

`GET /v1/models/{model}` 未命中返回 404 + `not_found` 信封。错误类型表是契约级(`conventions`),本次是有意扩展而非顺手改:OpenAI 生态 404 `model_not_found` 是事实标准,客户端据此区分「模型名拼错」与「请求格式错」;复用 `bad_request`(400)会误导重试逻辑。同步 conventions 表与 `protocol/statusByType`;未知类型仍按 internal(500) 兜底,负面测试钉住不回归。

## 2. key 生命周期

### 2.1 签发粒度:credit 额度 + 暴露已有执行链

`tpm_limit` 与配额闸的执行链路已在位(`KeyInfo` 限额全带,`Quota.exceeded` key 级闸先行),本变更补「入口暴露」并同步完成一次**额度单位重定义**(2026-09-12 用户裁决,推翻初版「暴露 μ 列」方案):

- **内部计量基座:μ(1e-6 本币)→ credit(1 credit = **本位币** 1e-9;本位币 = metering.settlement,CNY|USD)`,迁移 0007 三列重命名 + 存量 ×1000 换算。动机:①廉价档($0.003/百万Token)在 1e-9 下逐 token 恰为整数 credit(3);②credit 锚定本位币而非各上游账单币种——否则同一配额池在 CNY/USD 模型下消耗速率随汇率漂移,语义自相矛盾;异币种报价加载期按 fx_usd_cny_milli 一次换算。
- 精度上界刻意止于 1e-9:市面报价 ≤6 位小数全覆盖再留三档余量;再细一位,int64 按 32MB 入站包络(≈1e7 token × $200/M = 2e18)即逼近溢出,需 128 位中间量——过度设计,明确不做。
- 价目**表示层不变**:仍是「元/美元主单位十进制字符串」(≤8 位小数),ParsePrice/FormatPrice 恒等往返;控制台价目页零改动。
- 缺省 0 = 不限,沿用全仓「零值即缺省」惯例;不配新字段时 standalone 零回归(rate=基座定义,无换算旋钮)。

### 2.2 过期:datetime-local 选择器 + 单点判定,不动缓存协议

`api_keys` 增 `expires_at` 可空列(迁移 0006)。判定设计:

- `KeyInfo` 增 `ExpiresAt`;判定收口在 **verify 成功之后、挂 ctx 之前**的单点,以当前时钟比对。查库缓存(10s TTL)命中的是完整 `KeyInfo`,过期判定对缓存命中路径同样逐请求执行——**过期时刻是不可变字段,缓存只影响 DB 变更可见性,不影响时间函数的精确性**,因此无需为此调用 `Invalidate`,缓存协议零改动。
- 过期 → `auth_failed`(401)。语义上过期与无效/吊销同为「凭据失效」,归同一错误类型(错误类型表零膨胀);但 **message 区分「API key 已过期」**——用户动作是重签,而非找管理员查吊销,可区分性是排障效率。
- `mk2-` 平台 key 的 `expires_unix` 在 `ParsePlatformKey` 内已判,构造 `KeyInfo` 时 `ExpiresAt` 置零(已验过,不重复判);两型 key 语义统一收口到单点。
- **传输格式 RFC3339 UTC,交互形态 datetime-local**:管理 API 存储层收发 RFC3339;控制台签发表单与编辑框用 `datetime-local` 选择器(本地时区,人类可读),前端负责本地 ↔ UTC 转换——运营者永远不需要手写机器时间格式。**不强制未来时间**(允许补录与测试构造,格式合法即可)。

### 2.3 运行态调整:PATCH 而非 PUT

新增 `PATCH /admin/v1/keys/{id}`,字段 `rpm`/`tpm_limit`/`daily_quota_credits`/`expires_at`(局部更新:省略不动,`expires_at` 显式 null 清除)。选 PATCH:资源含不可再生字段(id/secret),整体 PUT 必然引入「漏带字段即抹掉」的脚枪;限额调整是局部语义,PATCH 面最小。写后 `Invalidate()`——限额是 **DB 可变状态**(与过期不同),必须走既有吊销同款失效协议,下一请求新限额。未知 key 404(`not_found`),非法值 400 整单拒绝。不新增 enabled/启停概念:`status` 列已可禁用且 DELETE 已够,启停进 backlog 等真实需求。

### 2.4 明确不做(防 scope creep)

- **key 级模型白名单**:授权语义,当前无消费者(租户内 key 即信任边界);参数正确性已有治理层兜底。等第一个真实需求,设计先行。
- **key 启停(enabled)**:`status` 列 + DELETE 覆盖现状;新增生命周期态需要 UI/审计配套,收益不成比例。
- **兑换码/支付/多用户/渠道余额查询**:控制面产品领地(ADR-0005),宪法非目标。

## 3. 指标深度

### 3.1 三组序列与观测点

| 序列 | 类型 | 标签 | 观测点 |
|---|---|---|---|
| `muniu_tokens_total` | counter | `direction`(prompt/cached_prompt/completion)、`provider`、`model` | 计量落账处(此处 usage 三段有终值,含估算行) |
| `muniu_semantic_cache_queries_total` | counter | `model`、`result`(hit/miss/skip) | 缓存判定分支:实际检索=hit/miss,缓存未参与(全局/租户关、tools 请求等)=skip |
| `muniu_first_token_duration_ms` | histogram | `provider`、`model` | 流式首帧到达(仅流式;非流式不观测,总延迟已有 `muniu_request_duration_ms`) |

- `direction` 枚举与 `usage_logs` 三段计量口径逐字对齐(cached_prompt 单列:缓存折扣是经济语义,混进 prompt 即丢失);指标值与落账行同源,含 tiktoken 估算行(口径=usage_logs,不承诺第二精度)。
- TTFB 桶 1ms 起步:压测实测流式首字节 p50≈1.13ms,`DefBuckets`(5ms 起)会把最感兴趣区间吞进第一个桶。
- **成本序列明确不做**:计费口径的唯一权威是 usage_logs/控制台;指标另立成本口径即双源,漂移比缺失更危险。监控看流量与延迟,对账看库。

### 3.2 基数界与禁用等价

标签组合受配置界(provider × 别名 × 3 向),无请求级标签(不放 request_id/模型全名)。指标禁用时经既有 `Enabled()` 门禁零开销,行为与今天逐字节等价——契约测试扩展覆盖「禁用=无新序列、转发行为不变」。

## 4. 兼容与回归承诺

全部为加法:不配新字段、指标关闭、不调新端点时,既有行为与测试零变化(既有全量测试零改动通过 = 闸门)。对外契约变化仅两处,均为扩展非破坏:`/v1` 新增只读端点;错误类型表 +`not_found`。
