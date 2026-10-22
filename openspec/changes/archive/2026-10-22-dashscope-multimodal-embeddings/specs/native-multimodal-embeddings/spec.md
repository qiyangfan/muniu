# native-multimodal-embeddings Specification

## Purpose

通过百炼原生多模态向量 API 透传 `qwen3-vl-embedding` 与 `tongyi-embedding-vision-flash`，使视觉与文本 embedding 请求在共享鉴权、治理、换道和计量链路下保留供应商 wire 语义。

## ADDED Requirements

### Requirement: 百炼原生多模态入站路径

系统必须 (MUST) 提供 `POST /api/v1/services/embeddings/multimodal-embedding/multimodal-embedding`。请求必须 (MUST) 包含非空 `model` 路由别名及非空 `input.contents`；其他顶层字段、模态部件和参数按原始 JSON 保留。未知别名必须 (MUST) 按现有 `model_not_found` 行为拒绝。

#### Scenario: 支持别名进入数据面

- **WHEN** 已认证客户端以存在的多模态别名提交合法 native request
- **THEN** 请求进入该别名的支持候选集并按原生协议转发

#### Scenario: 不存在的别名拒绝

- **WHEN** 请求中的 `model` 不存在于 `model_routes`
- **THEN** 返回 `model_not_found`，不触达上游

### Requirement: 显式出站能力与候选选择

provider 只有在配置有效的 `native_multimodal_embeddings_url` 且适配器实现 native 多模态 embeddings 能力时，才可服务本 lane。系统必须 (MUST) 在每个候选尝试前检查能力并跳过不支持的候选；不得 (MUST NOT) 把 native request 发往 OpenAI `/embeddings`。别名没有任何支持候选时必须 (MUST) 返回 `400 multimodal_embeddings_not_supported` 且上游零调用。

#### Scenario: 缺少声明时拒绝

- **WHEN** 路由存在但所有 provider 均未声明 native 多模态 embeddings URL
- **THEN** 返回 `400 multimodal_embeddings_not_supported`，不触达任一上游

#### Scenario: 换道只经过支持候选

- **WHEN** 路由依次含不支持候选、支持候选，或首个支持候选返回可换道错误
- **THEN** 不支持候选不产生出站调用，后续支持候选按现有判定表参与换道

### Requirement: 请求与响应原生透传

出站 URL 必须 (MUST) 使用该 provider 显式配置的原生多模态 endpoint，鉴权必须 (MUST) 使用该候选 key 的 Bearer 头。出站 JSON 除顶层 `model` 改为候选 upstream model 外必须 (MUST) 与客户端原始 JSON 逐字节一致。上游成功响应的状态码、端到端响应头与 body 必须 (MUST) 原生返回，不转换为 OpenAI `data[].embedding`；hop-by-hop 头不得 (MUST NOT) 转发。

#### Scenario: Qwen 融合参数原样透传

- **WHEN** 请求包含 `qwen3-vl-embedding` alias、图文 `input.contents` 与 `parameters.enable_fusion=true`
- **THEN** 上游收到的 body 仅将 `model` 改为候选模型名，响应保持百炼原生 `output.embeddings` 结构

#### Scenario: Tongyi 多图请求原样透传

- **WHEN** 请求包含 `tongyi-embedding-vision-flash` alias 与 `multi_images` 输入
- **THEN** 上游收到完整 `multi_images` 结构，网关不翻译为 OpenAI input 数组

### Requirement: 原生错误与换道

系统必须 (MUST) 对完整缓冲的同步响应按现有 embeddings failover 判定表处理，不得在向客户端提交首字节后换道。不可换道的上游错误或支持候选耗尽时，必须 (MUST) 返回最后一个上游 HTTP 响应的状态码、端到端响应头与原始 body。尚未取得上游 HTTP 响应的传输错误使用 muniu 现有错误信封。

#### Scenario: 上游 4xx 错误原样返回

- **WHEN** 百炼返回不可换道的 native JSON 4xx 错误
- **THEN** 客户端收到相同状态码及逐字节相同的 body，不经过 muniu 错误信封改写

#### Scenario: 可换道错误后成功

- **WHEN** 首个支持候选返回现有判定表中的可换道状态，后续支持候选成功
- **THEN** 只向客户端返回后续成功响应，失败候选按现有 embeddings 规则留观测行

### Requirement: 数据面治理等价

新路径必须 (MUST) 接入既有 data-plane middleware，执行 API key 鉴权、模型白名单、RPM/TPM、日配额、插件和用量记录。插件必须 (MUST) 至多执行一次；若插件改写请求体，系统必须 (MUST) 重新验证 `model` 与 `input.contents`，并按改写后的 alias 重新路由、校验白名单。该 lane 不参与语义缓存；请求级 cost budget 按既有 embeddings 边界旁路。

#### Scenario: 白名单拒绝在上游调用前发生

- **WHEN** 有效 key 的模型白名单不包含该请求 alias
- **THEN** 返回 403，插件与上游均零调用

#### Scenario: 插件改写模型后复校验

- **WHEN** 插件将请求改写为另一个 alias
- **THEN** 网关对最终 alias 重新查路由和白名单，仅执行一次插件钩子

### Requirement: Native usage 与模态计量

成功响应必须 (MUST) 原样返回，同时从 native usage 提取计量。统一 `prompt_tokens` 必须 (MUST) 表示全部输入模态 token 总数；`vision_tokens` 必须 (MUST) 表示其中图像/视频 token 子集；本 lane 的 `completion_tokens` 与 `cached_tokens` 必须 (MUST) 为 0。无法解析、缺失或不自洽的 usage 必须 (MUST) 标记 `estimated=true`，不得伪造视觉 token 数。

#### Scenario: Qwen usage 分解

- **WHEN** Qwen usage 含 `input_tokens`(文本)、`image_tokens`(图像/视频)及可选 `total_tokens`
- **THEN** 计量行记录 `prompt_tokens=input_tokens+image_tokens`、`vision_tokens=image_tokens`；若提供的 total 与分项不符则标记 estimated

#### Scenario: Tongyi usage 分解

- **WHEN** Tongyi usage 含总量 `input_tokens` 与 `input_tokens_details.image_tokens`
- **THEN** 计量行记录总输入 token 与视觉子集；文本 token 等于总量减视觉子集

#### Scenario: Usage 缺失但业务响应成功

- **WHEN** 上游返回 2xx native body 但 usage 缺失或无效
- **THEN** 2xx body 原样交付并写入 `estimated=true` 观测行，不把未知视觉用量伪记为精确零
