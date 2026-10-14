# Spec Delta: api-key-lifecycle — key 级模型白名单

## ADDED Requirements

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
