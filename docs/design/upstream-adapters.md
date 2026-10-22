# 上游适配器与协议 Conformance(upstream-adapters)

> muniu 的差异化立身之本。本文的归一化表与 fixture 格式是契约;**矩阵中的供应商行为细节执行时必须以官方文档逐家核对后回填**(矩阵初稿按 2026-09 已知形态写就,标 ⚠ 处为待核对项)。

## 适配器分类学(ADR-0003)

```
internal/adapter/
├── adapter.go        Adapter 接口 + 注册表
├── openai.go         OpenAI 兼容基座(覆盖所有「OpenAI 兼容端点」供应商)
├── quirks/           per-provider quirk patch:只声明「与基座的差异」,不整家居覆写
│   ├── deepseek.go  moonshot.go  glm.go  qwen.go  doubao.go
└── bridge/
    └── anthropic.go  Messages API ↔ OpenAI 形态全翻译
```

三分法理由:国产五家(DeepSeek/Moonshot/GLM/Qwen/Doubao)主打 OpenAI 兼容端点,差异集中在**少数字段与流式形态**——用 quirk patch 表达差异,基座保持单一实现;Anthropic 是真异构协议,值得整家桥接。**禁止为「OpenAI 兼容家」复制整套适配器**(one-api 式 per-provider 全量实现 = 维护灾难)。

配置面 `providers[].adapter` 的值域单源是 `adapter.Types()`(= `adapter.For` 可实现集合),`internal/config` 不再手抄清单:历史上手抄过的 `gemini` token 无对应实现,配置能过校验却装配失败,已由 `retire-gemini-adapter-token` 退役。

## Adapter 接口(草案,实现时可微调签名;方法集为契约)

```go
// Adapter 把入站 OpenAI 形态请求翻译为上游调用,并把上游响应/流归一化回 OpenAI 形态。
type Adapter interface {
    // Prepare:改写出站请求(URL/头/body),含鉴权头注入(key 由 keypool 提供)
    Prepare(ctx context.Context, in *protocol.ChatRequest, out *Outbound) error
    // Classify:把上游错误响应归类为 failover 判定表所需的 ErrorClass
    Classify(status int, body []byte) failover.ErrorClass
    // NormalizeUsage:上游 usage 原始 JSON → 统一 Usage(归一表见 metering)
    NormalizeUsage(raw json.RawMessage) (protocol.Usage, error)
    // StreamEvent:单事件的读侧归一(计量用),返回透传字节(见「字段改写白名单」)
    StreamEvent(raw []byte) (passthrough []byte, meta EventMeta, err error)
}
```

## 归一化:统一 Usage 形态(计量与 conformance 的对齐基准)

| 统一字段 | OpenAI | Anthropic(桥接) | Gemini 原生(ADR-0015 已接入) | DeepSeek(quirk) | 备注置 |
|---|---|---|---|---|---|
| prompt_tokens | usage.prompt_tokens | usage.input_tokens | usageMetadata.promptTokenCount | prompt_tokens | — |
| completion_tokens | usage.completion_tokens | usage.output_tokens | usageMetadata.candidatesTokenCount | completion_tokens | — |
| reasoning_tokens | completion_tokens_details.reasoning_tokens | —(thinking 另计⚠) | thoughtsTokenCount | completion_tokens_details.reasoning_tokens | 缺失=0;Gemini 口径=completion 子集,不与 candidates 相加 |
| cached_tokens | prompt_tokens_details.cached_tokens | cache_read_input_tokens | cachedContentTokenCount | prompt_cache_hit_tokens | miss 类字段忽略 |

GLM/Qwen/Moonshot/Doubao:默认继承 OpenAI 列,quirk 层只覆盖实测差异(以 conformance fixture 实证后登记)。

## 字段改写白名单(客户端可见行为)

默认**零改写**:基座与 quirk 对流式事件只读不写(读侧归一仅供计量)。桥接型(当前仅 anthropic)是唯一改写者——它们必须把异构事件翻译为 OpenAI 形态(该翻译矩阵另立 `bridge/README.md`,随 Phase 2 编写并配 fixture)。此白名单的意义:兼容型供应商的客户端体验与直连**逐字节一致**,网关透明性可被 conformance 断言。

## 同构直通接口(protocol-native-passthrough;ADR-0014)

`adapter.NativePreparer` 可选能力接口(与 `EmbeddingPreparer` 同款,proxy 类型断言探测):

- `NativeProto()`:本适配器原生直通的入站协议(协议身份自述;桥接=messages,基座=responses)。
- `PrepareNative(NativeRequest, model, key)`:原始体直通出站——URL/鉴权头按既有声明,body 仅 model 字段原位手术(`protocol.RewriteModelInPlace`),其余逐字节保留。
- `NativeStreamEvent(data)`:直通流事件 meta-only 读侧——返回内部统一 chunk(计量/缓存拼包与翻译路径同缝)+ meta;客户端恒收上游原字节,本方法不改写 wire。
- `NormalizeNativeUsage(raw)`:直通响应原生 usage → 统一 Usage(计量口径与翻译路径一致)。
- `adapter.NativeFetcher.PrepareNativeFetch(respID, key)`:GET 状态查询代理出站(有状态 lane)。

**直通不属「字段改写白名单」约束域**:白名单治理的是翻译路径的字段改写;直通零改写(仅 model 手术),保真即其契约。bridgegate(工具多轮边界)仅作用于翻译路径——直通候选按原生协议正常服务工具多轮。

### 百炼原生多模态 Embeddings

新 lane 使用百炼原生 `POST /api/v1/services/embeddings/multimodal-embedding/multimodal-embedding`，不进入 OpenAI `/v1/embeddings` 基座。当前范围只有 `qwen3-vl-embedding` 和 `tongyi-embedding-vision-flash`：

| 模型快照 | 当前输入与向量模式 | 官方参数边界（百炼校验） |
|---|---|---|
| `qwen3-vl-embedding` | text、image、video；支持多个 image 条目；默认独立向量，`parameters.enable_fusion=true` 时融合 | text 最长 32,000 Token；图片单张 ≤10 MB；视频 ≤50 MB；一次最多 20 个内容元素、10 张图片、1 个视频；维度为 2560/2048/1536/1024/768/512/256（默认 2560） |
| `tongyi-embedding-vision-flash` | text、image、video、`multi_images`；当前快照仅独立向量 | 固定 768 维，不接受 `dimension`；媒体格式、批次 token 及数量限制以当前所用区域的百炼 API 文档为准 |

图片可使用官方允许格式的 URL 或 Base64 Data URI；视频使用可公开访问的 URL，具体格式随模型而异。网关不下载媒体，也不仿制模型参数表：除解析非空路由 `model` 与 `input.contents`、执行 16 MiB HTTP 请求体上限外，`parameters`、媒体 URL/格式/尺寸/数量、token 上限与维度均由百炼校验。原始 JSON 保留未知字段，只原位替换顶层 `model`；成功和上游错误响应均按 native wire 透传。超出网关请求体上限的 Base64 请求在上游调用前被拒绝，使用媒体 URL 可避免把媒体字节放进网关请求体。

`tongyi-embedding-vision-flash-2026-03-06` 是不同模型快照，支持融合向量且有不同参数边界，当前不在路由范围内。不要把旧快照别名自动升级到新快照；新增快照须单独核对模型能力、usage 口径和 conformance fixture。

官方核对来源（2026-10-22）：[百炼多模态向量 API](https://help.aliyun.com/zh/model-studio/multimodal-embedding-api-reference)、[百炼向量化与兼容接口说明](https://help.aliyun.com/zh/model-studio/embedding)、[Tongyi Embedding Vision Flash 模型信息](https://help.aliyun.com/zh/model-studio/tongyi-embedding-vision-flash)。参数边界可能随区域和模型快照调整，改动前应复核官方文档；网关保持透明，由上游返回参数错误。

## 请求参数治理(ADR-0010,请求侧;与上方响应侧白名单正交)

上节白名单管的是**响应/流事件侧**,原文不变。**请求侧**新增模型能力治理,分界口诀:同一供应商的不同模型行为不同 → 能力层;同一参数跨协议表达不同 → 方言层(仍归 quirk/bridge,不入目录)。

- **约束来源 = 配置声明制,零内置隐式知识**:模型个性约束(参数黑名单、思考锁、输出上限等)全部来自 YAML 静态拓扑的显式声明(与 providers/model_routes/pricing 同源,ADR-0004);未声明的模型或参数 = 零干预、原样透传。
- **治理点 = 每次 failover attempt 出站前**,按「目标 provider+model」解析约束执行;治理为幂等纯函数,换道到新家后按新模型重新治理——参数兼容是 model_routes 换道正确性的组成部分,不是客户端责任。
- **动作语义**:声明了约束才干预(如 drop 不支持参数、clamp 数值越界);干预必留痕(结构化日志 + 客户端可见响应头);默认宽松放行,strict 模式可配为 400。具体字段设计为实现级,不锁死本文。
- **负面用例底线**(测试先行):声明参数被 drop、数值被 clamp、思考锁生效、换道后按新模型重治理、未声明零干预(透传字节不变)。

## 协议矩阵(2026-09-09 官方文档核对后更新;⚠=仍待实证项)

| 供应商 | 接入形态 | 鉴权 | 流式 reasoning | usage 特有 | 已知坑(登记后入 quirks+fixture) |
|---|---|---|---|---|---|
| OpenAI | openai 基座 | Bearer | reasoning 模型 ⚠ | prompt/completion_tokens_details | 组织头可选;beta 头漂移 ⚠ |
| DeepSeek | openai 基座 | Bearer | `reasoning_content` delta,与 content 逐帧互斥、推理先出(thinking_mode 文档确认) | 顶层 `prompt_cache_hit_tokens`/`prompt_cache_miss_tokens`(非 OpenAI details 嵌套);`completion_tokens_details.reasoning_tokens` 确认存在 | reasoning 与 content 互斥出现 |
| Moonshot | openai 基座 | Bearer(已迁 platform.kimi.com;jwt 方案确认废弃) | 思考模型 `delta.reasoning_content` 确认 | 流式 usage **必须** `stream_options.include_usage` 才返回([DONE] 前独立帧、choices 空),否则缺失→估算路径;`cached_tokens` 为 OpenAI 风格命名 | 多轮须回传历史 reasoning_content;流中断可能无 usage 帧 |
| GLM(智谱) | openai 基座 | Bearer 直连 API key(确认无需 JWT) | `delta.reasoning_content` 确认(thinking.type=enabled) | 流式 usage 返回方式 ⚠ 待实证 | `request_id` 回传头 ⚠ |
| Qwen(DashScope 兼容模式) | openai 基座 | Bearer,base_url=dashscope.aliyuncs.com/compatible-mode/v1 | `enable_thinking` 开启,`delta.reasoning_content` 确认(首帧 reasoning 为空串) | `stream_options.include_usage` 支持:空 choices usage 终帧含 reasoning_tokens | 原生模式开思考须 incremental_output=true(兼容模式无此问题) |
| Doubao(Arc) | openai 基座 | Bearer,base_url=ark.cn-beijing.volces.com/api/v3 | `delta.reasoning_content` | `stream_options.include_usage` | model 填模型名或推理接入点 ID(ep-xxx);文档站 JS 渲染无法逐字引用,按文档结构与公开 SDK 形态核对 |
| Anthropic | bridge(已落地;工具多轮请求侧翻译完整,anthropic-bridge-tool-roundtrip) | x-api-key + anthropic-version(2023-06-01) | thinking_delta → reasoning_content(桥接翻译) | input/output/cache_read_input_tokens → prompt/completion/cached_tokens | 事件类型机(message_start/content_block_delta/message_delta/message_stop);tool_calls→tool_use 块/role=tool→合并 tool_result/tool_choice 含 none;官方文档区域屏蔽,按公开契约实现待复核(见 bridge/README) |
| Gemini | **gemini 原生 bridge(ADR-0015 已落地)** | x-goog-api-key | parts[].text/thought → content/reasoning_content(functionCall/functionResponse 双向完整) | usageMetadata.prompt/candidates/Cached/ThoughtsTokenCount → 统一 Usage(双形态兼容) | REST v1beta `:generateContent` / `:streamGenerateContent?alt=sse`;tool_choice 完整映射含 NONE;流无 [DONE](StreamCloseDone 契约) |

> 本矩阵只管协议事实。各家官方端点(base_url)、订阅 Plan 端点红线与冒烟路径的
> 运营清单见 [docs/provider-presets.md](../provider-presets.md)。
>
> **Anthropic 桥接鉴权为声明制**(config `providers[].auth`,ADR-0010 同哲学):缺省
> x-api-key(协议标准,官方 Anthropic/SDK 口径;对其发 Bearer 形态 API key 即 401,
> Bearer 仅留给 OAuth);`auth: bearer` 改发 `Authorization: Bearer <key>`——国内分支
> 官方文档普遍以 AUTH_TOKEN(Bearer)演示,目录中的分支预设已按此声明。零自动探测,
> 分支实际接受头以 `MUNIU_SMOKE_PROVIDER=anthropic`(配 `MUNIU_SMOKE_AUTH=bearer`)
> 真上游冒烟为准。

## Conformance 测试集(独立开源资产,目录即契约)

```
conformance/
├── README.md               数据集说明:来源、回放方法、贡献规范
├── schema.json             fixture 结构定义(JSON Schema)
├── openai/chat-basic.json  每家每场景一文件:<provider>/<scenario>.json
├── deepseek/reasoning-stream.json
├── deepseek/usage-cache-tokens.json
└── ...
```

fixture 形态:`{ "provider": "...", "scenario": "...", "source_url": "官方文档锚点", "inbound": {OpenAI 形态请求}, "upstream_raw": {录制的上游原始响应/SSE 序列(脱敏)}, "expect": {归一化断言: usage/字段/passthrough 等价性} }`。

纪律:
1. fixture 来自**官方文档与抓包脱敏**,不入真实 key、不含用户数据;
2. CI 只回放 fixture(打 fake 上游),真上游冒烟走 `make smoke` 手动档;
3. 新 quirk 修复必须先补一个能复现 bug 的 fixture(回归钉死),这正是「测试集是资产、网关是消费者」的落地;
4. `source_url` 必填——每个字段断言可溯源,这是与「拍脑袋兼容」的分界线。

## 桥接工具多轮显式边界(upstream-egress-batch 契约)

Anthropic 桥接的响应侧 `tool_use → tool_calls` 翻译完整;**请求侧多轮中途标记**(`role=tool` 消息或 assistant 消息带非空 `tool_calls`)的翻译未实现。此类流量对桥接候选的处理不是透传也不是一律拒绝,而是:

1. **检测**:候选出站前判定请求是否处于工具循环中途;仅声明 `tools` 参数不触发(首轮合法)。
2. **候选适配性豁免**:桥接候选跳过,继续换道(failover 判定表新格);发生在取 key 之前,**不计 key EMA/冷却**(适配性不匹配 ≠ 上游故障,与 adapter_missing、strict 治理拒绝同口径);结构化日志留痕,客户端无感。
3. **终态信封**:全部候选耗尽且无任何真实上游错误 → 400 `bridge_tool_roundtrip_unsupported` 自解释信封(而非上游 400 转发的 `upstream_error`)。

判定位置在候选循环取 key 之前(`internal/proxy/bridgegate.go`);桥接型适配器以 `adapter.Bridger` 接口标记,新增桥接家实现标记即自动纳入判定。

## Gemini 接入方向(upstream-egress-batch 裁决)

走 Gemini 官方 **OpenAI 兼容端点**(openai 基座 + quirk 覆写实测偏差),**不新建原生 adapter**;接入前先真上游冒烟记录偏差清单,quirk 只覆盖实测项(零内置隐式知识,与 ADR-0010 同哲学);fixture 从冒烟实录脱敏装配。美元报价经本位币结算链处理(metering.md「本位币与 credit」:`metering.settlement` + `fx_usd_cny_milli`)。

配置面落地口径(2026-09-30,openspec change `retire-gemini-adapter-token`):`adapter` 字段**不接受** `gemini`,值域单源为 `adapter.Types()`;Gemini 接入写 `adapter: openai` + 兼容层 base_url,预设目录已按此收录(alias `gemini`)。上方 Usage 表的「Gemini 原生」列仅为原生协议备查,不代表已接入能力。

> **2026-10-11 转正注记(ADR-0015,gemini-native-protocol)**:`adapter: gemini` 以真实实现回归值域单源(原生 REST v1beta 整家实现,真异构桥接型、翻译完整、Bridger 如实声明)——上节口径被本 ADR 取代;「openai 兼容层接入」仍是合法形态(基座直用),两者并存按 provider 配置选择。下方「Gemini 接入方向」节保留为兼容层形态的历史裁决记录。
