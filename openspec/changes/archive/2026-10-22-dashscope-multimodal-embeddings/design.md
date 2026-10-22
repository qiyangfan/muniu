# Design

## D1 入站协议：沿用百炼原生端点路径

新增 `POST /api/v1/services/embeddings/multimodal-embedding/multimodal-embedding`，与百炼原生 HTTP 路径相同。请求使用百炼原生 JSON：`model` 是 muniu 路由别名，`input.contents` 承载文本、图片、视频或 `multi_images`，`parameters` 承载 `dimension`、`enable_fusion`、`fps`、`instruct` 等上游参数。

这条 lane 不翻译成 OpenAI Embedding DTO。入站仅解析路由必需的非空 `model` 与 `input.contents`；其他字段由原始 JSON 保留并送到上游。路径与请求结构由百炼官方 API 定义；官方文档注明融合向量不支持 OpenAI 兼容接口。

## D2 出站能力：显式 URL + 可选接口

provider 可选配置完整 URL：

```yaml
providers:
  - name: dashscope
    adapter: qwen
    base_url: https://dashscope.aliyuncs.com/compatible-mode/v1
    native_multimodal_embeddings_url: https://dashscope.aliyuncs.com/api/v1/services/embeddings/multimodal-embedding/multimodal-embedding
```

完整 URL 与现有 chat/text-embedding `base_url` 分开，避免把 compatible-mode 路径误用于原生 API，也允许部署配置区域域名或兼容网关地址。字段缺省表示该 provider 不声明此能力；不会根据 `adapter: qwen` 或模型名自动推断端点。

出站使用与 provider 声明一致的 Bearer API key。请求体只对顶层 `model` 原位替换为当前候选上游模型名，保持键顺序、空白、转义、`input`、`parameters` 及未知字段不变。每个 failover 候选独立检查能力；不支持该 lane 的候选从候选集跳过。别名无任何可用候选时返回本地 `400 multimodal_embeddings_not_supported`。

`adapter` 暴露可选 `MultimodalEmbeddingPreparer` 能力，不扩展必须由所有适配器实现的主接口。初始实现挂在 Qwen/DashScope 适配器；不新增依赖或把整套请求复制为第二个供应商适配器。

## D3 模型与原生 wire 边界

- `qwen3-vl-embedding`：文本/图片/视频输入；独立向量为默认形态，`enable_fusion=true` 请求融合向量。
- `tongyi-embedding-vision-flash`：文本/图片/视频输入，包含其原生 `multi_images` 输入；此模型快照只承诺独立向量。新快照 `tongyi-embedding-vision-flash-2026-03-06` 不在本 change 范围。
- 模型专属参数、图片/视频可访问性、格式、尺寸、输入数量和维度限制由百炼校验。muniu 不静默删除、改写或推测这些字段；上游错误按原生 JSON 返回。
- 请求体上限沿用 embeddings 当前 16 MiB 限制。大文件应使用上游支持的可访问 URL；超过网关上限按本地 bad request 拒绝。

## D4 响应、错误与候选语义

此 lane 为同步非流式。完整缓冲响应后再决定是否换道，避免客户端已收到响应后重试。上游成功响应的状态码、端到端响应头与响应体按原生形态返回，响应体不转换成 `data[].embedding`。

可换道错误继续使用现有 embeddings 判定表；不可换道错误或所有支持候选失败时，返回最后一个上游 HTTP 错误的状态码、端到端响应头和 body。只有未取得上游 HTTP 响应的本地/传输错误才使用 muniu 现有错误信封。hop-by-hop 头不透传。

本地鉴权、白名单、限流、配额、模型不存在和插件拒绝继续使用 muniu 统一错误契约；不会伪装成百炼上游错误。

## D5 控制面边界

新路径接入 `dataPlaneMW`，使用现有模型路由和 key 级白名单；插件只执行一次。插件改写后重新解析并校验 `model`，最终出站请求仍只改写上游模型名。用量每候选 attempt 按现有规则观测。

此 lane 不触发 chat 语义缓存，不做图片/视频下载或缓存；请求预算继续按现行 embeddings 边界旁路。TPM 使用原生 usage 总输入 token 计数，RPM、日配额与现有 lane 一致。

## D6 usage 与模态计价

统一 usage 中 `prompt_tokens` 表示所有输入模态 token 总数，`vision_tokens` 是其中图像/视频 token 子集；`completion_tokens` 与 `cached_tokens` 对此 lane 为 0。原始 provider response 始终不因 usage 解析而改写。

- Qwen 原生 usage：`usage.input_tokens` 是文本 token，`usage.image_tokens` 是视觉 token，`usage.total_tokens` 是总输入 token。校验字段自洽后，分别映射总数与视觉子集。
- Tongyi 原生 usage：`usage.input_tokens` 是文本与视觉 token 总数；`usage.input_tokens_details.image_tokens` 提供视觉子集。缺失的视觉明细按无精确模态拆分处理，保持响应透传并将该行标记 `estimated=true`。
- 任一模型 usage 缺失、字段类型非法或不自洽时，响应仍成功透传；用量行按现有估算/缺失 usage 纪律标记 `estimated=true`，不伪造视觉 token 数。
- 价目 `input` 适用于文本 token，`vision_input` 适用于视觉 token，币种与单位沿用 token 价目(credit/百万 token)。缺少 `vision_input` 时回落 `input`。该 lane 成本为 `ceil(text_tokens × input_rate / 1e6) + ceil(vision_tokens × vision_input_rate / 1e6)`，视觉与文本分段向上取整；无价目仍按既有契约记零。

`usage_logs` 新增 `vision_tokens`；其数据须穿过 SQLite、cluster 缓冲/续传、远程计量汇及管理查询。迁移只追加，不改既有迁移。

## D7 兼容性与来源

- `/v1/embeddings` 与既有 provider 不增新行为；旧价目中 `vision_input` 缺省，所有现有请求仍使用原价目公式。
- provider URL 是静态拓扑字段，需进入本地 YAML、SIGHUP 热替换和控制面拓扑 payload；provider 未改变时池与运行态按现有规则复用。
- 新管理价目字段必须在 YAML 与 DB 运行态覆盖、价目 GET/PUT、OpenAPI 类型和控制台编辑中一致。

官方依据(核对日期：2026-10-22)：

- [百炼多模态向量 API](https://help.aliyun.com/zh/model-studio/multimodal-embedding-api-reference)：原生端点、请求/响应结构、两种模型的参数和 usage 结构。
- [百炼向量化](https://help.aliyun.com/zh/model-studio/embedding)：多模态向量融合不支持 OpenAI 兼容接口。
- [tongyi-embedding-vision-flash 模型信息](https://help.aliyun.com/zh/model-studio/tongyi-embedding-vision-flash)：模型模态及当前/新快照区分。
