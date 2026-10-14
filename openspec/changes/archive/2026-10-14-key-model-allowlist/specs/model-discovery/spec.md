# Spec Delta: model-discovery — 按 key 白名单过滤的发现视图

## MODIFIED Requirements

### Requirement: 模型列表端点契约

`GET /v1/models` 必须 (MUST) 返回 OpenAI list 形态(`{"object":"list","data":[{id,object:"model",created,owned_by}]}`),条目集合必须 (MUST) 等于转发拓扑快照中 `model_routes` 的别名键集**经请求 key 白名单过滤后的视图**:key 无白名单 = 全量键集;key 有白名单 = 键集与白名单的交集(别名在拓扑但不在白名单不得出现)。按 `id` 升序输出;`owned_by` 恒为 `muniu`,`created` 为快照加载时刻。端点必须 (MUST) 经数据面鉴权(Bearer,`mk-`/`mk2-` 同规),但必须 (MUST) NOT 消耗 RPM/TPM 额度、NOT 触发配额闸、NOT 产生计量落账。

#### Scenario: 列表与拓扑同源

- **WHEN** 拓扑热替换新增别名 X 并生效后,以无白名单 key 请求 `GET /v1/models`
- **THEN** 返回列表包含 X(且不含已移除别名),无需重启或缓存失效

#### Scenario: 白名单 key 列表为过滤视图

- **WHEN** 白名单 `["m-flash"]` 的 key 在拓扑含 `m-flash`/`m-pro`/`m-embed` 三别名时请求 `GET /v1/models`
- **THEN** 返回列表仅含 `m-flash`,且拓扑中不在白名单的别名不出现

#### Scenario: 配额耗尽仍可发现模型

- **WHEN** 某 key 当日配额已超限(转发请求会被 402 拒绝)后以该 key 请求 `GET /v1/models`
- **THEN** 请求正常返回其白名单视图列表(诊断链路不被配额闸阻断)

#### Scenario: 未鉴权拒绝

- **WHEN** 无效/缺失 Bearer 凭据请求 `GET /v1/models`
- **THEN** 返回 401 `auth_failed` 信封,与 `/v1/chat/completions` 鉴权失败形态一致

### Requirement: 单模型查询与 404 契约

`GET /v1/models/{model}` 对存在且在请求 key 白名单视图内的别名必须 (MUST) 返回单模型对象;对存在于全局目录但不在 key 白名单的别名必须 (MUST) 返回 403,错误信封 `error.type=forbidden`、`error.code=model_not_allowed`(与数据面转发拒绝同码同形);对不在全局目录的别名必须 (MUST) 返回 404 + `not_found` 错误信封(错误类型表扩展,`protocol` 与 conventions 同步);未命中判定以别名(路由键)为准,不匹配上游模型名。

#### Scenario: 命中返回单对象

- **WHEN** 别名 X 存在于拓扑快照且在请求 key 白名单视图内,请求 `GET /v1/models/X`
- **THEN** 返回 `id=X` 的单模型对象,字段与列表条目一致

#### Scenario: 在目录但白名单外返回 403

- **WHEN** 别名 `m-pro` 存在于拓扑但不在请求 key 白名单内,请求 `GET /v1/models/m-pro`
- **THEN** 返回 403,信封 `type=forbidden`、`code=model_not_allowed`,与转发路径同形

#### Scenario: 未命中返回 not_found 信封

- **WHEN** 请求 `GET /v1/models/不存在别名`
- **THEN** 返回 404,信封 `error.type=not_found`,含 request_id,不外泄内部细节
