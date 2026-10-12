# gemini-native-outbound Specification

## Purpose
Gemini 原生出站适配器(`adapter: gemini`):把规范 chat 请求翻译为 Gemini REST `v1beta` wire 形态(`:generateContent` / `:streamGenerateContent?alt=sse`),响应/流/usage 归一回 OpenAI 形态。ADR-0003 三分法中的「真异构协议」整家实现;曾被 `retire-gemini-adapter-token` 因无实现退役的类型名,本次以真实实现回归值域单源。

## Requirements

### Requirement: 请求翻译契约(Prepare)

`adapter: gemini` 的出站请求必须 (MUST) 按冻结映射表(upstream-adapters.md Gemini 节)构造:`POST {base}/v1beta/models/{上游模型}:generateContent`;system 提升为顶层 `systemInstruction`;tools → `functionDeclarations`;`tool_choice` 必须完整映射到 `functionCallingConfig.mode`(含 NONE);`generationConfig` 同义字段与多模态 `inlineData` 按映射表;鉴权头按声明注入 `x-goog-api-key`。字段形态非法必须 (MUST) 显式报错归 bad_request,禁止静默丢弃致上游在无工具状态下作答。逐字段映射细节以映射表与 fixture 为准。

#### Scenario: 逐字段翻译 fixture

- **WHEN** 携带 system/tools/tool_choice/generationConfig 参数的 chat 请求经 gemini 适配器 Prepare
- **THEN** 出站 wire 逐字段等于冻结 fixture(URL、头、body)

#### Scenario: tool_choice none 原生映射

- **WHEN** chat 请求携带 `tool_choice: "none"` 且声明 tools
- **THEN** 出站 `functionCallingConfig.mode=NONE`(Anthropic 桥接的同名登记缺口在 Gemini 适配器上不存在)

#### Scenario: 多模态 inlineData

- **WHEN** chat 消息 content 含 image_url 且 URL 为 data URL
- **THEN** 出站为 `inlineData{mimeType,data(base64)}`,字节与源一致

#### Scenario: 工具多轮完整服务不豁免

- **WHEN** 携带工具多轮上下文(role=tool 消息 / assistant.tool_calls)的请求发往 gemini 候选
- **THEN** functionCall/functionResponse 翻译完整、正常出站,不触发桥接工具多轮适配性豁免(声明式缺口:gemini 声明翻译完整,anthropic 桥接维持现状豁免行为不变)

### Requirement: 响应归一(RewriteResponse 与 NormalizeUsage)

非流式响应必须 (MUST) 归一为 OpenAI chat completion 形态:`candidates[0].content.parts` 的 text/functionCall → content/tool_calls;`finishReason` 按冻结映射表(STOP→stop 或 tool_calls、MAX_TOKENS→length、SAFETY/RECITATION/PROHIBITED_CONTENT→content_filter、其余未知值兜底 stop);`usageMetadata`(promptTokenCount/candidatesTokenCount/cachedContentTokenCount/thoughtsTokenCount)必须 (MUST) 归一为统一 Usage(归一表见 upstream-adapters);`promptFeedback.blockReason` 非空必须 (MUST) 归类为 content_filter 且不产出内容。

#### Scenario: 非流式归一 fixture

- **WHEN** 上游返回含 text + functionCall parts 与 usageMetadata 的 GenerateContentResponse
- **THEN** 归一结果 choices/finish_reason/tool_calls/usage 逐字段等于冻结 fixture

#### Scenario: 安全拦截诚实呈现

- **WHEN** 上游返回 `promptFeedback.blockReason=SAFETY` 无 candidates
- **THEN** 归一为 content_filter 错误类(非空内容伪造),客户端收到明确信封

### Requirement: 流式事件机(StreamEvent)

流式读侧必须 (MUST) 将 `alt=sse` data 帧(chunk)翻译为 OpenAI chunk 并产出正确 EventMeta(HasContent/IsUsage);Gemini 流式无 `[DONE]` 终止标记——流关闭即 IsDone(计量与首 token 判定语义与既有适配器一致,usage 多帧非零合并);流中 `{error:{...}}` 帧必须 (MUST) 报错交由 proxy 断言出口/换道归类。字段改写遵守「桥接型为唯一改写者」白名单——gemini 为桥接型(翻译为 OpenAI 形态),Bridger 标记必须 (MUST) 如实声明。

#### Scenario: 事件序列 fixture

- **WHEN** 上游 SSE 输出文本增量帧、functionCall 帧、含 finishReason 与 usageMetadata 的终帧后关流
- **THEN** 翻译产出 delta 帧 → tool_calls 帧 → finish_reason+usage 帧,meta 序列(HasContent/IsUsage/IsDone)与 fixture 一致

#### Scenario: 流中错误帧

- **WHEN** 流中出现 `{error:{code:429,status:"RESOURCE_EXHAUSTED"}}` 帧
- **THEN** 事件机报错,proxy 按归类执行换道/断言出口,不产出伪造内容帧

### Requirement: 错误归类(Classify)

上游错误响应 `{error:{code,message,status}}` 必须 (MUST) 按 failover 判定表归类:INVALID_ARGUMENT/400 → bad_request(不换道)、429/RESOURCE_EXHAUSTED → 配额类、500/503/UNAVAILABLE/DEADLINE_EXCEEDED → server_error 类(换道);归类行为进入 failover 判定表新格并留契约测试。

#### Scenario: 判定表归类

- **WHEN** 分别收到 400 INVALID_ARGUMENT、429 RESOURCE_EXHAUSTED、503 UNAVAILABLE
- **THEN** Classify 依次产出 bad_request、配额类、server_error 类,与判定表逐格一致

### Requirement: 值域回归与装配

`gemini` 必须重新进入适配器值域(`adapter.Types()` 由 `adapter.For` 真实构造集合派生);配置 `adapter: gemini` 加载与装配必须 (MUST) 成功;既有 6 类适配器的行为必须 (MUST) 与本变更前逐字节一致;EmbeddingPreparer 不实现(经类型断言探测归 bad_request,与 anthropic 同款)。

#### Scenario: 配置装配成功

- **WHEN** 配置声明 `adapter: gemini` 的 provider 并启动
- **THEN** 加载通过、装配成功、chat 流量正常出站;值域一致性测试(types_test)通过

#### Scenario: 既有类型零改动

- **WHEN** 以既有 6 类适配器回放全部协议层既有测试
- **THEN** 结果与本变更前逐字节一致
