# Spec Delta

## MODIFIED Requirements

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
