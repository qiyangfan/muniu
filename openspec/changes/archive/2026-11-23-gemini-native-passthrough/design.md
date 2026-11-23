# Design

## 决策 1：路径判定复用既有 `decideNativePath`，声明粒度 = 适配器类型

`decideNativePath` 已按「入站协议标签 = 适配器 `NativeProto()` 自述」判定；gemini 只需两处到位即自动入表：①handler 以 `InboundGemini` 组装 `inboundNative`；②`Gemini` 实现 `NativePreparer` 且 `NativeProto()=InboundGemini`。

不加 `providers[].native_gemini` 声明：声明制的实证依据是「OpenAI 兼容 ≠ responses 端点可用」（百炼 responses 端点缺口），而 `adapter: gemini` 本身就是原生 REST v1beta 实现，适配器类型即能力自述（与 messages×anthropic 同款）。跨协议候选（openai 系适配器）的 `NativeProto` 不匹配，自然回落翻译。

## 决策 2：`NativeRequest` 增加 `Stream` 字段

Gemini 流式语义编码在 URL 方法后缀（`:streamGenerateContent?alt=sse`），请求体无 `stream` 字段；`PrepareNative(in, upstreamModel, apiKey)` 仅凭 `Raw` 无法判定。给 `NativeRequest` 增 `Stream bool`（由 `nat.stream` 装填）是唯一最小改法。

备选否决：①新增 `NativeStreamPreparer` 可选接口——为单协议引入接口分叉，后续新增同类协议还得再分；②从 Raw 解析 `stream`——gemini wire 不存在该字段；③handler 在 URL 里塞标记——污染 `NativeRequest` 契约且跨包不可见。

## 决策 3：惰性拒绝只收 `content_part_unsupported`

`TranslateGeminiRequest` 的拒绝面分两类：

- **结构性非法**（`input_invalid`）：路径模型缺失、contents 为空、role 非法、functionResponse 先于 call——原生上游同样会拒，维持 Fatal 即时 400。
- **翻译能力缺口**（`content_part_unsupported`）：fileData、音频/视频 inlineData、system 非文本部件——Gemini 原生支持，属「直通可服务」，改为 Ineligible。

`stream_form_unsupported`（缺 `alt=sse`）维持 handler 前置 400：这是 v1 端点契约（ADR-0015 决策 2），不属于翻译缺口。

## 决策 4：writer 恒等模式与「不合成帧」

Gemini 流无 `[DONE]`，翻译路径依赖 `StreamCloseIsDone` + `finishClean` 合成终帧。直通路径上游已自带终帧，合成即污染。因此：

- 流式 writer：`native` 时 `Write` 直写并 flush；`finish`/`finishClean`/`complete` 全部短路。
- 非流式 writer：`native` 时成功体与错误信封都直写；`flush` 短路。
- 模式位与 messages/responses 同不变式：首字节提交前由 `setWriterNativeMode` 切换，已提交绝不切换。

## 决策 5：计量与缓存

- 非流式：`absorbNativeBody` 对 `InboundGemini` 读 `usageMetadata`（Gemini 响应无 `usage` 键）；缺失时按既有估算兜底。
- 流式：`NativeStreamEvent` 复用 `StreamEvent`（无状态形态）产出 canonical 内部帧，`meta` 驱动首 token 与 usage 累计；工具序号只影响 canonical 内部帧，计量不依赖。
- 缓存：`canonicalFromNative` 增 gemini 分支，走既有 `RewriteResponse` 生成 canonical 形态；原生体 + 协议标签 + 表单标记照旧双存；同构命中恒等回放由既有 `replayHit` 的协议标签比较自动覆盖。

## 决策 6：failover 与失格升格

- 文本请求：gemini 候选失败换道到 openai 候选 → 该 attempt 判定翻译（协议不匹配）→ 单跳 canonical，行为与变更前一致。
- 失格请求（如 fileData）：别名含 gemini 候选时先直通；换道落翻译候选时 `prepareAttempt` 走既有 `upgradeIneligible` 升格 400，信封为 Gemini 形态（新增分支），上游零调用。
- 会话缓存亲和：`resolveStick` 保持排除 gemini lane（`nat.proto==InboundMessages` 才参与），无行为变化；oauth lane 收窄对 gemini 的跳过语义不变。
- **治理评估基准**：gemini 直通的原始体里采样参数在 `generationConfig` 嵌套且命名不同（`maxOutputTokens`/`topK`），按原始体评估会让能力目录约束全部失效。gemini native attempt 的治理改取 canonical（字段已映射为 chat 名），与翻译路径同一判据；命中干预即回落翻译路径并按约束改写——控制面判定结果不因路径切换而改变。raw 评估保留给字段名与 canonical 同域的协议（messages/responses）。

## 备选与否决

- **gemini 专用直通接口**：与决策 1 的复用原则冲突，N 个协议 N 套接口。
- **直通也走 array-form**：改变 ADR-0015 已冻结的端点契约，另立变更。
- **直通时跳过 canonical 翻译**：治理/缓存指纹/插件/计量均以 canonical 为键载体，跳过即控制面分裂（ADR-0014 决策 1）。
