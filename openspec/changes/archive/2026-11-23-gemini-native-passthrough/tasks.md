# Tasks

## 1. 协议层：Gemini 双形态翻译（测试先行域）

- [x] 1.1 先写失败测试：`TranslateGeminiRequestEx` 保留原始体与 `InboundGemini` 标签；`content_part_unsupported`（fileData、音频/视频、system 非文本部件）产出 Ineligible 且可轻量取回路径模型与流式标志；结构性非法维持 Fatal；`go test ./internal/protocol/ -run TestTranslateGeminiRequestEx` 先红
- [x] 1.2 实现 Ex 入口（既有 `TranslateGeminiRequest` 包装语义不变）；`go test ./internal/protocol/ -race` 全绿

## 2. adapter：Gemini 直通能力（测试先行域）

- [x] 2.1 先写失败测试：`PrepareNative` 出站 URL（非流式 `:generateContent` / 流式 `:streamGenerateContent?alt=sse`）、`x-goog-api-key`、请求体逐字节保留；`NativeStreamEvent` 产出 canonical 内部帧与正确 `EventMeta`；`NormalizeNativeUsage` 归一 `usageMetadata`；`go test ./internal/adapter/ -run TestGeminiNative` 先红
- [x] 2.2 实现 `NativeProto`/`PrepareNative`/`NativeStreamEvent`/`NormalizeNativeUsage` 与 `NativeRequest.Stream` 字段；`go test ./internal/adapter/ -race` 全绿

## 3. proxy：路径判定、writer 与失格升格（测试先行域）

- [x] 3.1 判定表测试增补 gemini 行（gemini×gemini=直通、gemini×其他=翻译、插件改写回落）；`go test ./internal/proxy/ -run TestDecideNativePath` 先红后绿
- [x] 3.2 先写失败测试再实现 handler 原始体保留 + 原生上下文 + 惰性拒绝分流；`go test ./internal/proxy/ -race` 全绿
- [x] 3.3 先写失败测试再实现两个 writer 的直通模式位（恒等写、零合成帧、升格 400 与上游错误原样）；`go test ./internal/proxy/ -race` 全绿
- [x] 3.4 非流式原生计量的 `usageMetadata` 识别与 `canonicalFromNative` gemini 分支；`go test ./internal/proxy/ -race` 全绿

## 4. 组合测试

- [x] 4.1 e2e：非流式直通保真（含 fileData 失格部件原样到上游、响应逐字节回写）；流式直通保真（帧逐字节、零合成终帧、无 `[DONE]`）；failover 换道回落翻译（文本请求成功反向映射为 Gemini 形态）；失格请求换道落翻译候选升格 400 且上游零调用；`go test ./internal/proxy/ -run TestGeminiE2E` 全绿

## 5. 文档与收尾

- [x] 5.1 文档：`proxy-and-streaming.md` 路径判定表增 gemini 行与 Gemini 节说明；`docs/adr/0015` 补 follow-up 落地注记；`npm`/定位文案（AGENTS.md、README、GitHub About）统一为多协议原生入站口径
- [x] 5.2 收尾门禁：`make check` 全绿；`openspec validate --all --strict` 通过；验收证据贴 HANDOFF
