# ADR-0015:Gemini 原生协议出入站

- 状态:已接受(2026-10-11;owner 点名立项——无原生 key,以 wire-format 对齐完成双向联调;ADR-0013 备选记录「Gemini 入站出现真实需求再立法」由本次触发解除)

## 决策

Gemini 原生 wire 双向内置化:**入站** `POST /v1beta/models/{model}:generateContent` 与 `:streamGenerateContent?alt=sse`(边缘翻译复用既有管道,ADR-0012/0013 同款);**出站** `adapter: gemini` 原生整家实现(REST v1beta,`x-goog-api-key` 鉴权;曾被 retire-gemini-adapter-token 因无实现退役的类型名以真实实现回归值域单源)。

1. **内置化而非外部工具,fixture 单源双向对拍**:转换器本体=入站边缘翻译层,不是外部进程;`conformance/gemini/` 语料为唯一 wire 契约源,被两方向反向消费,回环对拍(出站∘入站=恒等,可翻译子集域)作漂移探测器。语料按公开契约保守合成,逐条标注 ⚠ 待真 key/模拟端点复核。
2. **REST v1beta 非 gRPC**;出站鉴权 `x-goog-api-key`(官方支持形态中唯一不把 key 暴露进 URL/日志的一种);入站适配 `x-goog-api-key` 头与 `?key=` 两形态,后者读后从 URL 剥离(访问日志/上游/错误回显零泄漏);流式仅 `alt=sse`,array-form 显式 400。
3. **无 key 联调拓扑(三层)**:CI 层零网络 fixture;真模型联调=双实例自替身(实例 B=本变更入站+真实上游即「模拟原生 Gemini 端点」,实例 A=本变更出站指向 B);owner 自备的任何 Gemini 兼容端点可直接替换 B——真 key 到位后仅换 baseURL,契约零改动。
4. **声明式缺口(gemini 出站为「翻译完整」的桥接型)**:工具多轮请求侧翻译完整(functionCall/functionResponse 双向),`Bridger` 如实标记;bridgegate 升级为「Bridger ∧ ToolRoundtripComplete()=false」——anthropic 桥接声明不完整(现状豁免行为逐字节不变),gemini 声明完整(工具多轮正常出站不豁免)。备选「不实现 Bridger」否决(改写白名单分类学撒谎);「gate 硬编码 kind 清单」否决(登记表式硬编码,新增桥接必漏)。
5. **tool_choice 完整映射与 usage 转正**:functionCallingConfig.mode 原生支持 NONE(anthropic 桥接的同名登记缺口在 gemini 不存在),auto/ANY/NONE/具名全映射;usageMetadata 归一表从「备查」转正(thoughtsTokenCount 归 reasoning 口径,登记为 completion 子集语义);`promptFeedback.blockReason` 归 content_filter,禁止空内容伪造成功。

## 备选与否决

- **外部转换器拼装模拟端点(owner 初版方案)**:形态正确但外部 Node 工具不进仓(依赖白名单制),且只模拟 wire 不产生契约资产;内置化后外部工具降级为可选联调替身。
- **Vertex gRPC**:目标 JD 明确要求再立项(既有裁决沿用)。
- **gemini×gemini 直通并入本变更**:协议平等原则下同权,但属 passthrough 路由判定表增补一行的小变更,owner 指令触发(登记 follow-up)。

## 后果

- `internal/protocol/gemini.go`(双向翻译纯函数)、`internal/adapter/gemini.go`(五方法+声明)、`internal/proxy/gemini.go`(handler+writer)、`server.AdaptGeminiKey`、路由 `POST /v1beta/models/{modelMethod}` 进漂移门禁。
- 流式无 [DONE]:`StreamCloseDone` 可选能力声明(干净关流=正常收尾);无终止标记协议的干净收尾经 `streamFinalizer` 钩子合成协议终态(Gemini 终帧/Anthropic message_delta+stop/裸 chat 补 [DONE])。顺带根修:管道流前失败信封经协议 wrapper 改写时直写底层 writer(原实现二次进 tail 被丢弃,客户端空响应——messages wrapper 同源缺陷一并修复)。
- 登记限制(v1):`safetySettings`/`topK`/`candidateCount`/`seed`/`allowedFunctionNames`(多元素)静默丢弃;thought 部件展示形态映射;`countTokens`/`files`/`caching`/原生 embeddings 不提供;音频/视频与 fileData 入站显式拒绝;工具流式按收束帧发出(functionCall 为完整对象,无增量形态)。
