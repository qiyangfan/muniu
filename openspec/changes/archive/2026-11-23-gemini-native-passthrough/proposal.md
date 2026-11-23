# Proposal

## Why

ADR-0015 将「gemini×gemini 直通」登记为 owner 指令触发的 follow-up，至今未落地：Gemini 入站 handler 在边缘一次性翻译为 canonical chat 后丢弃原始体，`adapter: gemini` 未实现 `NativePreparer`。结果是 Gemini 客户端 → gemini 上游的双重换译（gemini→chat→gemini），fileData/safetySettings 等原生能力在翻译层被拒绝或丢弃，与 ADR-0014「协议平等、能直连就直连」的立法基线相悖。

owner 2026-11-23 再次点名「能直连就直连、能只转一把就只转一把，不要用 OpenAI 做固定中转」，触发本 follow-up。

## What Changes

- **gemini×gemini 直通**：Gemini 入站保留原始请求体与协议标签进入管道；`adapter: gemini` 实现 `NativePreparer`（适配器类型即协议身份声明，与 messages×anthropic 同款，零自动探测）。出站 URL 承载上游模型名（`/models/{model}:generateContent` 或 `:streamGenerateContent?alt=sse`），请求体逐字节等于客户端原始体，鉴权注入 `x-goog-api-key`。
- **路径判定表增补 gemini 行**：每 attempt 判定，failover 换道后对新候选重新判定；换道到非 gemini 候选回落既有翻译路径（单跳 canonical），行为与本变更前逐字节一致。
- **惰性拒绝**：`content_part_unsupported` 类翻译失格（fileData、音频/视频 inlineData、system 非文本部件）不再一律边缘 400——别名含 gemini 直通候选时按原生直通服务（上游原生支持）；仅当翻译路径实际被需要（别名无直通候选，或换道落翻译候选）时升格既有 400 Gemini 信封。别名无直通候选时行为逐字节不变。
- **writer 恒等模式**：Gemini 非流式/流式 writer 增加直通模式位——上游状态/头/体（流式逐帧）原样透传，不合成终帧、不改写错误信封；模式位仅首字节提交前可切换。
- **计量与缓存口径复用**：直通非流式从响应 `usageMetadata` 归一 usage；直通流式经 `NativeStreamEvent` 产出 canonical 内部帧供计量/首 token 判定，客户端恒收原字节；缓存双存（canonical 经既有 `RewriteResponse` 衍生 + 原生体 + 协议标签 + 表单标记）。
- **NativeRequest 增加 Stream 标志**：Gemini 流式语义在 URL 方法后缀而非请求体，`PrepareNative` 需要该标志构造出站 URL。
- **明确不做（v1）**：array-form 流式（`streamGenerateContent` 未带 `alt=sse` 维持 400 契约）、`countTokens`/`files`/`caching` 等原生端点、跨协议（gemini×其他）直通（保持单跳翻译）、会话缓存亲和对 gemini lane 的开放（沿用既有排除）。

## Capabilities

### Modified Capabilities

- `protocol-native-passthrough`：直通路径判定表 v1 范围增加 ③ Gemini 入站 × `adapter: gemini` 候选；直通 wire 保真与错误信封分域语义对 Gemini 生效。
- `gemini-inbound`：不可翻译内容部件从「恒即时报错」改为「惰性拒绝」——含直通候选时按原生直通服务，翻译路径实际需要时升格既有 400。

## Impact

- `internal/protocol`：新增 Gemini 双形态翻译入口（原始体保留 + 失格标记）；`NativeRequest` 增加 `Stream` 字段；`InboundGemini` 注释从「纯翻译型 lane」改为「原生直通 lane」。
- `internal/adapter`：`Gemini` 实现 `NativePreparer` 四方法（`NativeProto`/`PrepareNative`/`NativeStreamEvent`/`NormalizeNativeUsage`）。
- `internal/proxy`：Gemini handler 保留原始体并挂原生上下文；gemini 两个 writer 增加直通模式位；`upgradeIneligible` 增 Gemini 信封分支；非流式原生计量识别 `usageMetadata`；`canonicalFromNative` 增 gemini 分支。
- 文档：`docs/design/proxy-and-streaming.md` 路径判定表与 Gemini 节；`docs/adr/0015` 补 follow-up 落地注记；定位文案（AGENTS.md/README/GitHub About）统一为多协议原生入站口径。
- 无新依赖、无配置面变更、无存储迁移。
