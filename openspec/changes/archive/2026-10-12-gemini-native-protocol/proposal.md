# Proposal

## Why

owner 无原生 Gemini key,但需要完成 Gemini 原生 wire 格式的**出入站双向联调**(业务代码/客户端生态说 Gemini 协议:genai SDK、gemini-cli 等,只改 base_url 即可接入)。owner 给出的参考方案(外部转换器/模拟器拼装模拟 Gemini 端点)指出了正确形态:**不需要谷歌服务,只要 wire-format 对齐**。muniu 的最优落地是把这套能力内置化:入站翻译(转换器本体)+ 原生出站适配器(被测方向)+ 自建 fixture 契约(无 key 的 CI 冻结与真模型联调替身)。ADR-0013 备选记录「Gemini 入站:出现真实需求再立法」——owner 本次点名即触发条件成立。

## What Changes

- **新增 Gemini 原生入站**:`POST /v1beta/models/{model}:generateContent` 与 `:streamGenerateContent?alt=sse`,边缘翻译为规范 chat 复用既有管道(ADR-0012/0013 同款架构);模型别名取自路径;响应/流式/错误信封反向映射为 Gemini 形态。
- **新增 Gemini 原生出站适配器**:`adapter: gemini` 类型(曾被 `retire-gemini-adapter-token` 因无实现退役,本次以真实实现回归值域单源),REST `v1beta` wire 对齐:`:generateContent` / `:streamGenerateContent?alt=sse`,鉴权形态声明 `x-goog-api-key` 头。
- **fixture 单源双向对拍**:同一 Gemini wire 语料库(`conformance/gemini/`)被两个方向反向消费——入站反向映射与出站翻译互为漂移探测器;语料以公开契约保守合成,owner 提供的真实样例/模拟端点响应可随时充实。
- **无 key 联调拓扑(方案内置化)**:CI 层零网络 fixture;真模型联调走双实例——实例 B(本变更入站 + 真实上游如 dashscope)即「模拟原生 Gemini 端点」,实例 A(本变更出站)指向 B 完成 wire 全保真往返;任何外部 Gemini 兼容端点(owner 可自备)可直接替换 B。
- **鉴权适配**:入站 `x-goog-api-key` 头与 `?key=` 查询参数适配为平台 key(路径级中间件,`?key=` 读后从 URL 剥离,防访问日志泄漏)。
- **明确不做(v1)**:Vertex gRPC 形态、`countTokens`/`files`/`caching` 端点、Gemini 原生 embeddings、非 alt=sse 的 array-form 流式(显式 400,主流 SDK 均用 alt=sse)、音频/视频 inlineData;`safetySettings`/`topK`/`candidateCount` 静默丢弃(登记)。

## Capabilities

### New Capabilities

- `gemini-inbound`:Gemini 原生 REST 入站——请求/响应/流式翻译契约、鉴权头适配与剥离、错误信封形态、登记限制、管道等价性(治理/缓存/计量与 chat 同规)。
- `gemini-native-outbound`:Gemini 原生出站适配器——Prepare/Classify/NormalizeUsage/StreamEvent/RewriteResponse 五方法契约的 Gemini 实现、finishReason/usageMetadata 归一表、`adapter: gemini` 值域回归与装配。

### Modified Capabilities

- `adapter-taxonomy`:值域集合的示例枚举更新(gemini 回归为可实现类型;「未实现形态」的示例由 gemini 更换为其他不存在类型)——值域单源规则本身不变。

## Impact

- `internal/protocol/gemini_in.go`(入站 DTO 与双向翻译纯函数)、`internal/adapter/gemini.go`(出站适配器,ADR-0003 三分法中的「真异构协议」整家实现)、`internal/proxy/gemini.go`(入站 handler)、`internal/server`(AdaptGeminiKey,对齐 AdaptAnthropicKey 先例)、`cmd/muniu/main.go`(路由表进漂移门禁)。
- `conformance/gemini/`(fixture 语料,独立资产);`docs/`:ADR-0015、proxy-and-streaming.md「Gemini 入站翻译」节、upstream-adapters.md(Gemini 归一表从「备查」转正 + quirk/bridge 分类注记)、provider-presets.md。
- ADR-0013 备选记录解除(Gemini 入站立项);与 `protocol-native-passthrough` 提案无依赖关系——两能力齐备后,经 passthrough 路由判定表增补 gemini×gemini 一行即达同构零翻译(协议平等原则:Gemini 原生通道与 chat/messages/responses 同权,非二等公民)。
- 无新依赖(GO 与 web 白名单均不动);语义缓存键、计量落账、指标路径标签机制不变。
