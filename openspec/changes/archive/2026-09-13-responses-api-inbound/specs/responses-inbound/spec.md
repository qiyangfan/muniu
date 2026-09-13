## Purpose

OpenAI Responses API 入站(无状态翻译 v1,ADR-0012):`POST /v1/responses` 经边缘翻译复用既有转发管道;响应与流式事件反向映射;服务端不存储 response 对象。管道等价性——同一别名的 Responses 请求与等价 chat 请求,在治理/缓存/限流/计量上可观测行为一致。

## ADDED Requirements

### Requirement: 请求翻译契约

`POST /v1/responses` 必须 (MUST) 按冻结映射表(proxy-and-streaming.md「Responses 入站翻译」节)翻译为规范 chat 请求进入管道:instructions→system、input(string/items)→消息序列、function tools/tool_choice/max_output_tokens 同义映射;鉴权、配额、RPM/TPM 消费必须 (MUST) 与 chat completions 同规。翻译失败必须 (MUST) 返回 400 明确信封。

#### Scenario: 合法请求翻译生效

- **WHEN** 携带 instructions + message items + function tools 的 Responses 请求发往任一别名
- **THEN** 上游收到的为等价规范 chat 请求( fixture 断言逐字段),响应正常返回

#### Scenario: 危险字段显式拒绝

- **WHEN** 请求携带 `previous_response_id`、`background=true` 或不支持的 input item 类型(如 input_image)
- **THEN** 返回 400,code 分别为 `previous_response_id_unsupported`/`background_unsupported`/`input_item_unsupported`,不静默忽略、不触达上游

### Requirement: 响应与流式事件映射

非流式响应必须 (MUST) 映射为 Responses 对象(status=completed、output[]、usage,id 以 `resp_` 前缀复用 request_id);流式必须 (MUST) 输出最小事件集且满足顺序不变式:created 首帧、completed 终帧(携带 usage)、delta 仅出现于两者之间。

#### Scenario: 流式事件序列恒定

- **WHEN** 任一别名流式 Responses 请求完成
- **THEN** 事件序列为 created → output_item.added → (output_text.delta | function_call_arguments.delta)* → output_item.done → completed,fixture 断言全序列

#### Scenario: usage 如实传递

- **WHEN** 上游流式/非流式返回 usage
- **THEN** response(或 completed 事件)的 usage 与上游 usage 一致,计量落账行与等价 chat 请求同口径

### Requirement: 无状态边界

服务端必须 (MUST) NOT 存储 response 对象;`GET /v1/responses/{id}` 必须 (MUST) 恒返回 404 `not_found` 信封,文案如实说明无状态网关不存储。

#### Scenario: 查询已「创建」的 response

- **WHEN** 完成一次 Responses 请求后以其返回的 id 调用 GET
- **THEN** 恒定 404 not_found(不因刚刚请求过而存在)

### Requirement: 管道等价性与跨协议缓存

翻译后的规范请求必须 (MUST) 与等价 chat 请求共享全部管道语义:参数治理、插件挂点、限流/配额、failover 判定、计量落账口径一致;语义缓存键必须 (MUST) 取翻译后规范请求——同一别名的等价 chat 请求与 Responses 请求必须 (MUST) 命中同一缓存条目。

#### Scenario: 跨协议缓存命中

- **WHEN** 同一别名先以等价 chat 请求写入语义缓存,再以 Responses 形态发起相同语义请求
- **THEN** Responses 请求命中该缓存条目(计量记命中,零上游调用)

#### Scenario: 治理与计量等价

- **WHEN** 别名声明了能力约束且经换道到第二家
- **THEN** Responses 请求与等价 chat 请求接受相同的逐 attempt 治理,usage_logs 落账字段口径一致
