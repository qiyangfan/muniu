# Spec Delta

## Purpose

Gemini 原生 REST 入站:`POST /v1beta/models/{model}:generateContent`(及 `:streamGenerateContent?alt=sse`)经边缘翻译复用既有管道(ADR-0012/0013 同款架构),让说 Gemini 协议的客户端(genai SDK、gemini-cli 等)只改 base_url 即可经 muniu 使用任意出站上游——无需真实 Gemini key。

## ADDED Requirements

### Requirement: 请求翻译契约

Gemini 入站端点必须 (MUST) 按冻结映射表(proxy-and-streaming.md「Gemini 入站翻译」节)翻译为规范 chat 请求进入管道:`contents`(role user/model ↔ user/assistant)→ 消息序列、`systemInstruction` → system、`tools[].functionDeclarations` ↔ function tools、`generationConfig` 同义映射(temperature/topP/maxOutputTokens/stopSequences)、functionCall/functionResponse 部件 ↔ tool_calls/tool 消息、`inlineData`(data URL)→ chat 多模态 image_url。模型别名取自 URL 路径。鉴权、配额、RPM/TPM 消费必须 (MUST) 与 chat completions 同规;翻译失败必须 (MUST) 返回 400 Gemini 错误信封并携带机器码。

#### Scenario: 合法请求翻译生效

- **WHEN** 携带 systemInstruction + contents + functionDeclarations + generationConfig 的 generateContent 请求发往任一别名
- **THEN** 上游收到等价规范 chat 请求(conformance fixture 逐字段断言),响应正常返回

#### Scenario: 不支持项显式拒绝

- **WHEN** 请求携带 `fileData`/`fileUri` 部件、音频/视频 inlineData,或 streamGenerateContent 未带 `alt=sse`
- **THEN** 返回 400 机器码信封(`content_part_unsupported`/`stream_form_unsupported`),不静默忽略、不触达上游

#### Scenario: 路径别名与 chat 同规

- **WHEN** 路径中的模型名不存在于模型路由表
- **THEN** 返回与 chat `model_not_found` 同规的错误(信封为 Gemini 错误形态)

### Requirement: 响应与流式映射

非流式响应必须 (MUST) 反向映射为 GenerateContentResponse(candidates[].content.parts、role=model、`usageMetadata`、`finishReason`、modelVersion);流式必须 (MUST) 输出 `alt=sse` data 帧序列且满足顺序不变式:首帧存在、usage 出现于帧中、finishReason 终帧收尾;管道内错误必须 (MUST) 以 Gemini 错误信封(`{error:{code,message,status}}`)形态返回,SDK error.message 可读。

#### Scenario: 非流式响应 fixture 冻结

- **WHEN** 上游返回 chat completion(含 tool_calls 与 usage)
- **THEN** 客户端收到 candidates[].parts(functionCall/text)与 usageMetadata(promptTokenCount/candidatesTokenCount/totalTokenCount),全序列被 fixture 断言

#### Scenario: 流式事件序列恒定

- **WHEN** 任一别名流式 generateContent(alt=sse)请求完成
- **THEN** SSE 帧序列为内容增量帧 → 携带 finishReason 与 usageMetadata 的终帧,fixture 断言全序列;上游断流时以错误帧诚实收尾,不伪造正常终帧

### Requirement: 鉴权头适配与剥离

装配层必须 (MUST) 提供路径级鉴权适配(对齐 AdaptAnthropicKey 先例,先于 verifier):`x-goog-api-key` 头与 `?key=` 查询参数均适配为平台 Bearer key;`?key=` 必须 (MUST) 在读取后从 URL 中剥离——访问日志、上游请求、错误回显中不得 (MUST NOT) 出现该参数。

#### Scenario: 两种凭证形态放行

- **WHEN** 客户端分别以 `x-goog-api-key` 头、`?key=` 查询参数携带平台 key
- **THEN** 两种形态均通过数据面鉴权正常服务

#### Scenario: 查询参数凭证不泄漏

- **WHEN** 以 `?key=` 形态发起请求
- **THEN** 访问日志与结构化日志中的 URL 均不含 key 参数

### Requirement: 管道等价性与跨协议缓存

翻译后的规范请求必须 (MUST) 与等价 chat 请求共享全部管道语义:参数治理、插件挂点、限流/配额、failover 判定、计量落账口径一致;语义缓存键必须 (MUST) 取翻译后规范请求——同一别名的等价 chat 请求与 Gemini 请求必须 (MUST) 命中同一缓存条目。

#### Scenario: 跨协议缓存命中

- **WHEN** 同一别名先以等价 chat 请求写入语义缓存,再以 Gemini 形态发起相同语义请求
- **THEN** Gemini 请求命中该缓存条目(计量记命中,零上游调用)

#### Scenario: 治理与计量等价

- **WHEN** 别名声明了能力约束且经换道到第二家
- **THEN** Gemini 请求与等价 chat 请求接受相同的逐 attempt 治理,usage_logs 落账字段口径一致

### Requirement: 登记限制(v1 边界)

以下为 v1 显式边界,行为必须 (MUST) 与登记一致:`safetySettings`/`topK`/`candidateCount`/`seed` 静默丢弃(上游无对应语义,文档登记);thought 部件以展示形态映射(上游 reasoning_content → `thought:true` part,签名回传语义不成立);`countTokens`/`files`/`caching`/Gemini 原生 embeddings 端点不提供;array-form 流式(无 alt=sse)显式 400。

#### Scenario: 静默丢弃项正常服务

- **WHEN** 请求携带 safetySettings 与 topK
- **THEN** 请求正常翻译出站(字段不进入 canonical),行为与文档登记一致,不报错
