# Proposal

## Why

muniu 的多模态能力目前只在输入侧(四路 lane 的 image 块翻译全链),生成侧缺席:OpenAI 客户端调 `/v1/images/generations` 直接 404。同赛道网关(one-api/LiteLLM)全部支持 images 端点——这是入站矩阵的真实完整度缺口,不是超纲需求;且「按张计价」的价目维度演进(tokens=0 的计量行、价目表第二形态、预算预检从保守估算变为精确判定)正是本仓计量叙事的核心肌肉,属于求职作品集该有的资产。2026-10-13 二次裁决改判引入(原「拖价目迁移不值对价」论证在 completeness-IS-the-product 框架下失效);audio/TTS 二期另行裁决,不与本项目捆绑。

## What Changes

- 入站新 lane:`POST /v1/images/generations`(OpenAI 兼容同步形态),走 `dataPlaneMW` 全链(request_id/recover/access_log/auth/quota/ratelimit 免费复用),候选循环与 failover 判定表同 embeddings 通道(非流式单形态)。
- 出站能力接口:`adapter.ImagePreparer`(与 `EmbeddingPreparer` 同款可选能力,类型断言探测);不支持的适配器 → 400 `images_not_supported`(参数级问题,不换道)。
- per-image 计价维度:迁移新增 `image_pricing` 表(alias 主键 + 兜底单价 + size|quality 档位 JSON + 币种),YAML 静态兜底价目同款回落语义;管理 API `GET/PUT/DELETE /admin/v1/image-pricing` 与动态改价即时生效纪律对齐 `model_pricing`。
- 计量口径:images 行 tokens 全 0、`cost = n × 档位价`、`estimated=0`(价目表是 credit 单源,非上游 usage 换算);语义缓存不适用(付费非确定性生成,命中即错误)。
- 预算预检精确化:`max_request_cost_credits` 对 images 通道语义为「精确预检」——成本上界 n×价 在调用前完全可知,超界 402 `cost_budget_exceeded`、上游零调用(比 chat 的保守估算更强,无诚实旁路缺口)。
- 非幂等付费生成的换道语义钉子:上游 5xx/transport 失败 → 照判定表换道,但**只对最终交付的 2xx 计量计费**;响应中途断流(计费歧义)登记为诚实边界。
- 控制台价目页增 images 价目区;openapi.yaml 同步入站端点与管理端点。

## Capabilities

### New Capabilities
- `image-generation`:images 生成入站通道的行为契约——候选循环与能力探测、per-image 计量行口径、付费生成的换道计费纪律、预算精确预检。

### Modified Capabilities
- `pricing-management`:价目管理域新增 per-image 价目维度(表形态、YAML 回落、在线改价与热路径即时生效、管理 API 契约与既有 `model_pricing` 同纪律)。
- `request-cost-budget`:预检闸 Requirement 增补 images 场景——计价形态为精确单价(非 token 估算),超界拒绝语义与 chat 同族。

## Impact

- 迁移:0011 新增 `image_pricing` 表(追加式,不动旧表)。
- 代码:`internal/protocol/images.go`(DTO/解析)、`internal/proxy/images.go`(lane handler)、`internal/adapter`(ImagePreparer)、`internal/metering`(images 价目目录)、`internal/admin`(价目 CRUD)、`cmd/muniu/main.go`(路由表 +1 行)、`internal/store`。
- 依赖:零新增(纯 stdlib + 既有模块;无 SDK,无厂商客户端)。
- 契约兼容:纯增量,既有四 lane 与 token 价目行为零改动;openapi.yaml 只增不改。
- 待验证项:国内同步兼容 images 端点的具体家数(硅基流动确定有,智谱/其他待核)——按既有 smoke 纪律真上游实证后回填 provider-presets;dashscope wanx 类异步任务型(submit+poll)登记为明确不做(v1)。
