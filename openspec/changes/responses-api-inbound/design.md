# 设计:Responses API 入站 v1

## 1. 翻译点:管道入口之前的边缘适配

翻译器位于鉴权/配额/限流之后、转发管道入口之前(与 chat handler 汇入同一入口)。理由与备选取舍见 ADR-0012:管道内所有子系统零改动,双形态测试组合爆炸问题不存在;行为等价性用钉子测试锁死——同一别名下,Responses 请求与手工构造的等价 chat 请求,在治理/缓存/计量上的可观测行为一致。

## 2. 请求映射表(契约,fixture 钉死)

| Responses 入站 | 规范 chat | 说明 |
|---|---|---|
| `instructions` | system 消息 | 与 input 中显式 system 并存时 instructions 在前 |
| `input`(string) | 单条 user 消息 | 快捷形态 |
| `input[]` type=message | 同 role 消息 | content parts: text 拼接;其余类型 400 |
| `input[]` type=function_call | assistant.tool_calls | call_id/name/arguments 直映 |
| `input[]` type=function_call_output | role=tool 消息 | call_id 对应 |
| `tools[]` type=function | tools(function) | name/description/parameters 直映 |
| `tool_choice` | tool_choice | auto/none/required/具名同映射 |
| `max_output_tokens` | max_tokens | |
| `temperature`/`top_p`/`stream` | 同名透传 | |
| `model` | model(别名) | 解析与 chat 同规 |

**显式拒绝(400 + 明确 code,不静默)**:`previous_response_id`(`previous_response_id_unsupported`——静默忽略会静默丢失客户端上下文,属危险失败)、不支持的 input item 类型如 `input_image`(`input_item_unsupported`,v1 范围见 ADR-0012)、`background=true`。**忽略并文档化**:metadata 等非语义字段(白名单列于 proxy-and-streaming.md 新节)。

## 3. 响应与流式映射

- 非流式:chat completion → `{id, object:"response", status:"completed", model, output[], usage}`。`id` 复用 `resp_` 前缀 + 既有 request_id(追责链路与日志/usage_logs 一致);output[] 按 assistant 文本(message item + content[].output_text)与 tool_calls(function_call items)展开。
- 流式最小事件集:`response.created`(首帧)→ `response.output_item.added` → `response.output_text.delta` / `response.function_call_arguments.delta`(逐段)→ `response.output_item.done` → `response.completed`(终帧,含 usage)。顺序不变式:created 首帧、completed 终帧、delta 只出现在两者之间——fixture 断言全序列。
- reasoning:上游 `reasoning_content` 映射为 reasoning 输出项(只读),上游未提供即省略;不做持久化。

## 4. 无状态边界

`GET /v1/responses/{id}` 显式注册,恒 404 `not_found`,文案「无状态网关不存储 response」——比 mux 裸 404 诚实,客户端可编程区分。`previous_response_id` 拒绝理由同上。存储化(服务端会话)为 backlog 演进项,出现真实消费者再立法(ADR-0012 备选裁决)。

## 5. 与既有子系统的交互(零改动声明)

- **计量**:按上游 usage 落账,别名/牌价/三段口径与 chat 完全一致;翻译层不产生任何计量语义。
- **语义缓存**:键 = 翻译后规范请求 → **跨协议命中**(等价 chat 先行写入,Responses 检索命中),作为钉子测试场景。
- **治理/插件/failover**:均在翻译后的规范请求上运作,与 chat 无差异。
- **桥接交互**:Responses 工具多轮经 anthropic 出站时,触发 upstream-egress-batch 的显式边界(豁免/明确信封)——两 change 契约互相援引,不重复定义。

## 6. 明确不做(v1)

服务端存储/previous_response_id、background 模式、内置与 hosted 工具透传、多模态输入项、Anthropic Messages 入站(backlog)。全部登记演进条件,防 scope creep。
