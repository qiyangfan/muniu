# Proposal

## Why

muniu 已有 OpenAI 兼容 `POST /v1/embeddings`，但它固定出站到供应商 `base_url + /embeddings`。Kongming 需要的 `qwen3-vl-embedding` 与 `tongyi-embedding-vision-flash` 使用百炼原生多模态向量 API，原生路径、请求体和响应体均不同，当前无法经网关调用。Kongming HANDOFF 已记录文本 embedding 经 muniu 可用、这两款视觉模型仍直连百炼。

这是真实的数据面协议缺口：只加路由别名无法让现有 OpenAI embeddings handler 调用原生 API，也不能把 Qwen 文本与视觉 token 按不同单价准确落账。

## What Changes

- 新增百炼原生多模态向量入站路径 `POST /api/v1/services/embeddings/multimodal-embedding/multimodal-embedding`，接受原生请求格式并按模型别名路由。
- 为 provider 增加可选的完整出站 `native_multimodal_embeddings_url`；仅显式配置且适配器具备能力的候选可承载此 lane。请求只改写顶层 `model`，其余请求与响应保持原生 wire 形态。
- 初始 conformance 覆盖 `qwen3-vl-embedding` 与 `tongyi-embedding-vision-flash`。前者支持独立/融合向量，后者按该模型当前契约支持独立向量；模型特有参数由上游判定，不在网关内硬编码模型参数表。
- 多模态请求复用数据面鉴权、模型白名单、RPM/TPM、候选换道、插件、用量日志和现有 OpenAI embeddings 通道相同的控制面纪律；不进入语义缓存。
- 记录总输入 token 与视觉 token 子集；价目新增 `vision_input`，使 Qwen 的文本输入和图像/视频输入可以按不同单价结算。缺省视觉单价回落到 `input`，现有纯文本价目行为不变。
- 更新 topology 文件/控制面负载、usage 存储与查询、管理价目 API、OpenAPI、控制台价目表单及设计文档。

## Capabilities

### New Capabilities

- `native-multimodal-embeddings`: 百炼原生多模态向量入站、出站能力声明、wire 透传、候选行为与 native usage 计量。

### Modified Capabilities

- `data-plane-state-sync`: provider 的原生多模态出站 URL 随 topology 文件与控制面快照同步。
- `data-plane-metering-sink`: usage 行及各计量汇新增 `vision_tokens` 字段。
- `pricing-management`: token 价目增加可选视觉输入单价及其持久化、管理 API 与回落语义。

## Impact

- 配置与装配：`providers[].native_multimodal_embeddings_url`、配置校验、SIGHUP/控制面 topology 传播和适配器能力装配。
- 数据面：新 handler、OpenAPI 路径、fake 上游与 conformance fixtures；纯文本 `/v1/embeddings` 行为保持不变。
- 存储与管理面：追加迁移扩展 usage 和 token 价目；同步计量缓冲/汇、使用量读侧、价目 API 与控制台。
- 依赖：零新增。
- 本提案只定义实现计划，不包含代码实现、真实凭据冒烟或 Kongming 仓库改动。

## Non-goals

- 不改造 OpenAI `POST /v1/embeddings` 为百炼原生格式，不自动探测或转换两种请求协议。
- 不扩展到 `qwen2.5-vl-embedding`、`multimodal-embedding-v1`、Tongyi 2026-03-06 快照、其他厂商或异步任务型 API。
- 不在网关内下载/缓存图片或视频，不做模态内容校验、向量转换、融合/独立模式转换。
- 不新增 embeddings 请求级成本预算；继续遵循现有 embeddings 不在预算闸覆盖范围的登记边界。
- 不修改 Kongming 客户端、配置或模型注册表；跨仓切换由后续独立任务处理。
