# Spec Delta

## MODIFIED Requirements

### Requirement: 同构直通路径判定

入站协议与候选的原生协议一致时，该次尝试必须 (MUST) 走直通路径：出站请求体为客户端原始字节；响应原样透传。v1 范围：①Anthropic Messages 入站 × `adapter: anthropic` 候选；②Responses 入站 × 显式声明 `native_responses` 能力的候选（声明制，零自动探测——「OpenAI 兼容」不等于「responses 端点可用」，未声明候选维持既有 chat 翻译兜底）；③Gemini 入站 × `adapter: gemini` 候选（适配器类型即协议身份声明；流式方法由 URL 方法后缀承载，出站仍以客户端原始体为准）。路径判定必须 (MUST) 为每次尝试级——failover 换道后对新候选重新判定；入站协议与候选原生协议不一致时（如换道到未声明候选或非 gemini 候选），必须 (MUST) 走既有翻译路径。路径判定必须 (MUST) 由显式声明派生（入站路由 + 适配器类型/能力声明），禁止 (MUST NOT) 对请求体做协议自动探测。

#### Scenario: 同构流量直通服务

- **WHEN** Claude Code 以 `POST /v1/messages` 发送携带 image 内容块、thinking 回传、cache_control、stop_sequences 的请求，别名候选为 `adapter: anthropic` 的 provider
- **THEN** 请求不触发 `content_block_unsupported` 等翻译期拒绝，上游收到除 model 字段外逐字节等于客户端原始体的请求，行为与客户端直连该上游一致

#### Scenario: Responses 声明直通生效

- **WHEN** Responses 入站请求（含 input_image 部件）发往声明 `native_responses: true` 的候选
- **THEN** 该候选走直通路径，上游收到除 model 字段外逐字节等于客户端原始体的请求，图像部件原生透传

#### Scenario: Gemini 原生直通生效

- **WHEN** Gemini 客户端以 `POST /v1beta/models/{alias}:generateContent`（或 `:streamGenerateContent?alt=sse`）发送请求，别名候选为 `adapter: gemini` 的 provider
- **THEN** 该候选走直通路径，上游 URL 携带解析后的上游模型名，请求体逐字节等于客户端原始体，响应（流式逐帧）原样透传，不合成任何终帧或 `[DONE]`

#### Scenario: 未声明候选维持翻译兜底

- **WHEN** 同类 Responses 请求发往未声明 `native_responses` 的 openai 系候选
- **THEN** 维持既有 chat 翻译路径，行为与本变更前逐字节一致（image 部件按既有翻译语义拒绝）

#### Scenario: Gemini 换道跨协议回落翻译

- **WHEN** Gemini 入站请求的主候选（gemini）返回可换道错误，failover 切换到 openai 系候选
- **THEN** 第二候选按既有翻译路径服务（canonical chat 出站、响应反向映射为 Gemini 形态），客户端获得合法完整响应；Gemini 原始体不得发往非 gemini 候选

#### Scenario: 换道跨协议边界回落翻译路径

- **WHEN** 同一请求的主候选（anthropic）返回可换道错误，failover 切换到 openai 系候选
- **THEN** 第二候选按既有翻译路径服务（canonical chat 出站、响应反向映射为 Anthropic 形态），客户端获得合法完整响应
