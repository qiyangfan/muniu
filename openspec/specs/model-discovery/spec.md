# model-discovery Specification

## Purpose
OpenAI 兼容模型发现端点:客户端经 `GET /v1/models` 与 `GET /v1/models/{model}` 完成模型目录发现与连通性探测。目录与转发拓扑同源(同一快照),热替换同步可见;端点为只读元数据面,零上游调用、零计量、不消耗配额与限流额度。

## Requirements

### Requirement: 模型列表端点契约

`GET /v1/models` 必须 (MUST) 返回 OpenAI list 形态(`{"object":"list","data":[{id,object:"model",created,owned_by}]}`),条目集合必须 (MUST) 等于转发拓扑快照中 `model_routes` 的别名键集,按 `id` 升序输出;`owned_by` 恒为 `muniu`,`created` 为快照加载时刻。端点必须 (MUST) 经数据面鉴权(Bearer,`mk-`/`mk2-` 同规),但必须 (MUST) NOT 消耗 RPM/TPM 额度、NOT 触发配额闸、NOT 产生计量落账。

#### Scenario: 列表与拓扑同源

- **WHEN** 拓扑热替换新增别名 X 并生效后请求 `GET /v1/models`
- **THEN** 返回列表包含 X(且不含已移除别名),无需重启或缓存失效

#### Scenario: 配额耗尽仍可发现模型

- **WHEN** 某 key 当日配额已超限(转发请求会被 402 拒绝)后以该 key 请求 `GET /v1/models`
- **THEN** 请求正常返回完整列表(诊断链路不被配额闸阻断)

#### Scenario: 未鉴权拒绝

- **WHEN** 无效/缺失 Bearer 凭据请求 `GET /v1/models`
- **THEN** 返回 401 `auth_failed` 信封,与 `/v1/chat/completions` 鉴权失败形态一致

### Requirement: 单模型查询与 404 契约

`GET /v1/models/{model}` 对存在的别名必须 (MUST) 返回单模型对象;对不存在的别名必须 (MUST) 返回 404 + `not_found` 错误信封(错误类型表扩展,`protocol` 与 conventions 同步);未命中判定以别名(路由键)为准,不匹配上游模型名。

#### Scenario: 命中返回单对象

- **WHEN** 别名 X 存在于拓扑快照且请求 `GET /v1/models/X`
- **THEN** 返回 `id=X` 的单模型对象,字段与列表条目一致

#### Scenario: 未命中返回 not_found 信封

- **WHEN** 请求 `GET /v1/models/不存在别名`
- **THEN** 返回 404,信封 `error.type=not_found`,含 request_id,不外泄内部细节

### Requirement: 只读与零上游

模型发现端点必须 (MUST) 为纯只读:零上游供应商调用、零存储写路径、不接入插件挂点;拓扑快照为唯一数据源。

#### Scenario: 零上游副作用

- **WHEN** 任意频率调用模型发现端点
- **THEN** 不产生任何上游请求、`usage_logs` 行、语义缓存写或插件调用记录
