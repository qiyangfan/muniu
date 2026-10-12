# responses-inbound Specification

## Purpose
OpenAI Responses API 入站(无状态翻译 v1,ADR-0012):`POST /v1/responses` 经边缘翻译复用既有转发管道;响应与流式事件反向映射;服务端不存储 response 对象。管道等价性——同一别名的 Responses 请求与等价 chat 请求,在治理/缓存/限流/计量上可观测行为一致。

## Requirements

### Requirement: 请求翻译契约

`POST /v1/responses` 必须 (MUST) 按冻结映射表(proxy-and-streaming.md「Responses 入站翻译」节)翻译为规范 chat 请求进入管道:instructions→system、input(string/items)→消息序列、function tools/tool_choice/max_output_tokens 同义映射;鉴权、配额、RPM/TPM 消费必须 (MUST) 与 chat completions 同规。input 条目的 message 形态必须 (MUST) 接受官方规范内的全部合法形态:`content` 纯字符串快捷形态与部件数组形态等价(折叠为单 text 部件);message 条目 `type` 省略且携带 `role` 时必须 (MUST) 按官方缺省语义视为 `message`(官方 SDK 缺省产物即为该形态),不得因快捷形态误拒。翻译失败必须 (MUST) 返回 400 明确信封。

#### Scenario: 合法请求翻译生效

- **WHEN** 携带 instructions + message items + function tools 的 Responses 请求发往任一别名
- **THEN** 上游收到的为等价规范 chat 请求(fixture 断言逐字段),响应正常返回

#### Scenario: 危险字段显式拒绝

- **WHEN** 请求携带 `previous_response_id`、`background=true` 或不支持的 input item 类型(如 input_image)
- **THEN** 返回 400,code 分别为 `previous_response_id_unsupported`/`background_unsupported`/`input_item_unsupported`,不静默忽略、不触达上游

#### Scenario: 官方缺省形态放行

- **WHEN** input 条目为 `{"role":"user","content":"你好"}`(省略 type、字符串 content)或 `{"type":"message","role":"user","content":"你好"}`
- **THEN** 两者均翻译成功且 canonical 逐字节一致,行为与部件数组显式形态等价

#### Scenario: 缺省判定不越界

- **WHEN** input 条目省略 `type` 且无 `role`,或 `content` 为数字/null 等非法 JSON 形态
- **THEN** 维持 400 `input_invalid`(缺省判定仅限携带 role 的 message 条目,function_call/function_call_output 不做类型猜测)

#### Scenario: 快捷与显式形态缓存同构

- **WHEN** 同一语义请求分别以快捷形态(省略 type + 字符串 content)与显式形态(type + 部件数组)发起
- **THEN** 两者产生相同缓存指纹(后者命中前者写入的条目),计量落账口径一致

### Requirement: 响应与流式事件映射

非流式响应必须 (MUST) 映射为 Responses 对象(status=completed、output[]、usage,id 以 `resp_` 前缀复用 request_id);流式必须 (MUST) 输出最小事件集且满足顺序不变式:created 首帧、completed 终帧(携带 usage)、delta 仅出现于两者之间。

#### Scenario: 流式事件序列恒定

- **WHEN** 任一别名流式 Responses 请求完成
- **THEN** 事件序列为 created → output_item.added → (output_text.delta | function_call_arguments.delta)* → output_item.done → completed,fixture 断言全序列

#### Scenario: usage 如实传递

- **WHEN** 上游流式/非流式返回 usage
- **THEN** response(或 completed 事件)的 usage 与上游 usage 一致,计量落账行与等价 chat 请求同口径

### Requirement: 无状态边界

翻译路径必须 (MUST) NOT 存储 response 对象(别名无原生 responses 候选时,携带 `previous_response_id` 的请求必须 (MUST) 返回 400 `previous_response_id_unsupported` 自解释拒绝——chat 上游无服务端状态,翻译路径无可代理的状态语义)。原生 lane 上:成功响应的 id 必须 (MUST) 记入亲和表(id→provider,进程内 TTL + LRU 上限,集群形态复用共享后端);携带 `previous_response_id` 的请求必须 (MUST) 经粘性路由由亲和 provider 服务,亲和未知时必须 (MUST) 返回自解释错误,禁止 (MUST NOT) 盲发上游;状态请求必须 (MUST) 旁路语义缓存。`GET /v1/responses/{id}` 必须在亲和已知时代理转发亲和 provider 并原样返回,亲和未知时必须 (MUST) 恒返回 404 自解释信封。

#### Scenario: 查询已「创建」的 response

- **WHEN** 完成一次原生 lane 请求后,以其返回的 id 调用 GET
- **THEN** 代理转发亲和 provider,原样返回上游存储的 response 对象(粘性语义见 protocol-native-passthrough 能力)

#### Scenario: 查询未知 id 恒 404

- **WHEN** 以网关从未创建或亲和已丢失的 id 调用 GET
- **THEN** 恒返回 404 not_found 自解释信封(不因曾经代理过而可查)

#### Scenario: 翻译路径维持拒绝

- **WHEN** 携带 `previous_response_id` 的请求发往不含原生 responses 候选的别名
- **THEN** 返回 400 `previous_response_id_unsupported` 自解释信封,不触达上游

### Requirement: 管道等价性与跨协议缓存

翻译后的规范请求必须 (MUST) 与等价 chat 请求共享全部管道语义:参数治理、插件挂点、限流/配额、failover 判定、计量落账口径一致;语义缓存键必须 (MUST) 取翻译后规范请求——同一别名的等价 chat 请求与 Responses 请求必须 (MUST) 命中同一缓存条目。

#### Scenario: 跨协议缓存命中

- **WHEN** 同一别名先以等价 chat 请求写入语义缓存,再以 Responses 形态发起相同语义请求
- **THEN** Responses 请求命中该缓存条目(计量记命中,零上游调用)

#### Scenario: 治理与计量等价

- **WHEN** 别名声明了能力约束且经换道到第二家
- **THEN** Responses 请求与等价 chat 请求接受相同的逐 attempt 治理,usage_logs 落账字段口径一致
