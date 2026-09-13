# usage-metrics Specification

## Purpose
`/metrics` 运维序列补齐:token 流向、语义缓存判定分布、流式首字节延迟。全部序列从既有内部状态与计量口径导出(零新依赖、零新状态),标签基数受配置界约束;指标禁用时行为与开销逐字节等价于现状。

## Requirements

### Requirement: token 流向序列

`muniu_tokens_total`(counter,标签 `direction`/`provider`/`model`)必须 (MUST) 在计量落账时按 `direction ∈ {prompt, cached_prompt, completion}` 三向累加;口径必须 (MUST) 与 `usage_logs` 落账行同源(含 tiktoken 估算行,不承诺第二精度),不引入第二计量口径。

#### Scenario: 一次请求三向累加

- **WHEN** 一次带 usage 的请求落账(prompt=100/cached=20/completion=10,provider=p,model=m)
- **THEN** `muniu_tokens_total{direction="prompt",provider="p",model="m"}` +100,`cached_prompt` +20,`completion` +10

#### Scenario: 估算行同口径

- **WHEN** 流式 usage 缺失走 tiktoken 估算落账
- **THEN** 指标与该估算行同步累加(口径=落账行,无双源)

### Requirement: 语义缓存判定序列

`muniu_semantic_cache_queries_total`(counter,标签 `model`/`result`)必须 (MUST) 在缓存判定分支计数:`result ∈ {hit, miss, skip}`——实际检索命中=hit、检索未命中=miss、缓存未参与(全局或租户开关关闭、tools 请求等不适用场景)=skip。

#### Scenario: 三态分布可观测

- **WHEN** 依次产生一次命中、一次未命中、一次租户缓存关闭下的请求(同一别名 m)
- **THEN** `muniu_semantic_cache_queries_total{model="m",result="hit"|"miss"|"skip"}` 各 +1

### Requirement: 流式首字节延迟序列

`muniu_first_token_duration_ms`(histogram,标签 `provider`/`model`)必须 (MUST) 仅对流式请求在 upstream 首帧到达时观测,桶必须 (MUST) 自 1ms 起步(实测流式首字节 p50≈1.13ms,粗桶会吞掉感兴趣区间);非流式请求必须 (MUST) NOT 产生观测(总延迟已有 `muniu_request_duration_ms`)。

#### Scenario: 流式观测、非流式不观测

- **WHEN** 同一别名分别发起流式与非流式请求
- **THEN** 仅流式请求在直方图产生一次观测

### Requirement: 基数界与禁用等价

标签组合必须 (MUST) 受配置界约束(provider × 别名 × 枚举),禁止请求级标签(request_id 等);指标禁用时必须 (MUST) 零开销且转发行为与禁用前逐字节等价(经既有 `Enabled()` 门禁)。

#### Scenario: 禁用等价

- **WHEN** 关闭 metrics 配置后发起转发
- **THEN** 不产生任何新序列,请求行为、计量落账与关闭前一致(既有契约测试扩展覆盖)
