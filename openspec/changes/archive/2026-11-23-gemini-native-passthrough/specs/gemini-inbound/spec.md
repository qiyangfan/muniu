# Spec Delta

## MODIFIED Requirements

### Requirement: 请求翻译契约

Gemini 入站端点必须 (MUST) 按冻结映射表（proxy-and-streaming.md「Gemini 入站翻译」节）翻译为规范 chat 请求进入管道：`contents`/`systemInstruction`/`tools`/`generationConfig`/functionCall/functionResponse/inlineData 均按该表映射，模型别名取自 URL 路径。鉴权、配额、RPM/TPM 消费必须 (MUST) 与 chat completions 同规；结构性非法请求必须 (MUST) 返回 400 Gemini 错误信封并携带机器码。翻译能力缺口（`fileData`、音频/视频 `inlineData`、system 非文本部件）的对外行为按「翻译能力缺口的惰性拒绝」requirement 执行。

#### Scenario: 合法请求翻译生效

- **WHEN** 携带 systemInstruction + contents + functionDeclarations + generationConfig 的 generateContent 请求发往任一别名
- **THEN** 上游收到等价规范 chat 请求(conformance fixture 逐字段断言),响应正常返回

#### Scenario: 不支持项显式拒绝

- **WHEN** 请求携带 `fileData`/`fileUri` 部件、音频/视频 inlineData,或 streamGenerateContent 未带 `alt=sse`,且别名无 gemini 直通候选
- **THEN** 返回 400 机器码信封(`content_part_unsupported`/`stream_form_unsupported`),不静默忽略、不触达上游

#### Scenario: 路径别名与 chat 同规

- **WHEN** 路径中的模型名不存在于模型路由表
- **THEN** 返回与 chat `model_not_found` 同规的错误(信封为 Gemini 错误形态)

## ADDED Requirements

### Requirement: 翻译能力缺口的惰性拒绝

Gemini 入站请求包含翻译能力缺口部件时,沿用 protocol-native-passthrough 的惰性拒绝纪律:别名含 `adapter: gemini` 直通候选时必须 (MUST) 按原生直通服务,不经翻译、不拒绝;仅当翻译路径实际被需要(别名无直通候选,或换道落翻译候选)时必须 (MUST) 升格既有 400 `content_part_unsupported` Gemini 错误信封。别名无直通候选时行为与本变更前逐字节一致。`streamGenerateContent` 未带 `alt=sse` 的 400 `stream_form_unsupported` 契约不变(不属翻译缺口)。

#### Scenario: 直通候选原生服务

- **WHEN** 请求携带 `fileData` 或音频/视频 inlineData,且别名含 `adapter: gemini` 直通候选
- **THEN** 该候选走同构直通,上游收到客户端原始体,请求成功,不返回 `content_part_unsupported`

#### Scenario: 换道落翻译候选时升格

- **WHEN** 上述失格请求的主候选(gemini)返回可换道错误,且别名下一条候选为翻译型
- **THEN** 出站前升格为既有 400 `content_part_unsupported` Gemini 错误信封,翻译候选上游零调用

#### Scenario: 无直通候选维持即时报错

- **WHEN** 上述失格请求的别名不含任何 gemini 直通候选
- **THEN** 请求在进入候选循环前即时 400,行为与本变更前逐字节一致
