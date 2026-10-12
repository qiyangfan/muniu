# Anthropic 桥接翻译矩阵

桥接是唯一允许改写客户端可见字段的适配器(upstream-adapters「字段改写白名单」)。
源契约:Anthropic Messages API(`anthropic-version: 2023-06-01`,公开稳定形态;
官方文档在此网络区域屏蔽,按公开契约保守实现,可访问后逐条复核——HANDOFF 开放决策项)。

## 请求(OpenAI → Anthropic)

| OpenAI 形态 | Anthropic 形态 | 说明 |
|---|---|---|
| `Authorization: Bearer <key>` | `x-api-key: <key>` + `anthropic-version: 2023-06-01` | 鉴权头重写 |
| `messages[role=system/developer]` | 顶层 `system`(多条 `\n\n` 拼接) | system 提升 |
| `messages[user/assistant]` | `messages`(纯文本 content 折叠为字符串;工具上下文为块数组) | 连续同角色合并(纯文本 `\n\n` 连接、工具上下文块级合并;Anthropic 要求交替) |
| `assistant.tool_calls` | `content[].{type:"tool_use",id,name,input}`(input=arguments 对象) | 工具多轮请求侧翻译(anthropic-bridge-tool-roundtrip);空/非法实参落 `{}` |
| `messages[role=tool]` | user 消息 `content[].{type:"tool_result",tool_use_id,content}` | 连续多条 tool 消息合并进同一条 user(交替约束);tool_call_id 无配对报错归 bad_request |
| `max_tokens` | `max_tokens` | Anthropic 必填;OpenAI 缺省时填 4096 |
| `tools[type=function]` | `tools[{name,description,input_schema}]` | parameters 即 input_schema;字段形态非法时显式报错(归 bad_request),静默丢弃会让上游在无工具状态下作答 |
| `tool_choice`:`auto`/`required`/`{type:function,function:{name}}`/`"none"` | `{type:auto}`/`{type:any}`/`{type:tool,name}`/`{type:none}` | none 形态经真上游冒烟实证后映射(证据见验收记录;上游不接受则回退登记) |
| `top_p` | `top_p` | 原样透传 |
| `stop` | (未映射,登记限制) | Phase 3 范围外 |
| `stream` | `stream` | SSE |
| URL | `<base>/v1/messages` | 非 /chat/completions |

## 非流式响应(Anthropic → OpenAI)

| Anthropic | OpenAI |
|---|---|
| `id` / `model` | `id` / `model`(原样透出) |
| `content[type=text]` 拼接 | `choices[0].message.content` |
| `content[type=thinking]` | `message.reasoning_content`(非标准扩展字段,便于客户端展示) |
| `content[type=tool_use]` | `message.tool_calls[{id,name,arguments}]`(index 顺序) |
| `stop_reason`:end_turn/stop_sequence/pause_turn → `stop`;max_tokens/model_context_window_exceeded → `length`;tool_use → `tool_calls`;refusal → `content_filter` | `choices[0].finish_reason`(未知新值兜底 `stop`,保守取舍) |
| `usage.input_tokens / output_tokens / cache_read_input_tokens` | `usage.prompt_tokens / completion_tokens / (cached_tokens + prompt_tokens_details.cached_tokens)` |

## 流式事件机(Anthropic SSE → OpenAI chunk)

| Anthropic 事件 | OpenAI 翻译 | meta |
|---|---|---|
| `message_start`(usage.input_tokens/cache_read) | usage 帧 `{choices:[],usage:{prompt_tokens,cached_tokens}}` | IsUsage |
| `content_block_start(tool_use)` | 首个 tool_calls delta `{index,id,type:function,function:{name,arguments:""}}`(index=块序号) | HasContent |
| `content_block_start(text 等)` / `content_block_stop` / `ping` | 不产出(对客户端不可见) | — |
| `content_block_delta(text_delta)` | `delta.content` 增量 | HasContent(非空) |
| `content_block_delta(thinking_delta)` | `delta.reasoning_content` 增量 | HasContent(非空) |
| `content_block_delta(input_json_delta)` | `delta.tool_calls[index=块序号].function.arguments` 增量(并行调用按序号分离) | HasContent(非空) |
| `message_delta`(stop_reason + usage.output_tokens) | `finish_reason` + usage 帧(completion_tokens;prompt 分帧见下) | IsUsage |
| `message_stop` | `data: [DONE]` | IsDone |
| 空 data / 纯注释事件(SSE 心跳) | 不产出翻译帧也不报错(透传保真) | — |
| `type=error` | 报错 → proxy 断言出口 / 换道归类 | — |

事件机保持事件级无状态(Adapter 实例跨请求共享);usage 按消息生命周期分帧到达
(prompt 在 message_start、completion 在 message_delta),计量侧多帧非零合并。

## 已知限制(Phase 3 范围)

- `stop` 参数未映射(stop_sequences);图片等多模态 content 丢弃仅保留 text;
- **工具对话往返已完整翻译**(anthropic-bridge-tool-roundtrip):`assistant.tool_calls`→`tool_use` 块、`role:"tool"`→合并 user 消息的 `tool_result` 块、tool_call_id 配对校验;流式/非流式的响应侧 tool_use 翻译本就完整;
- thinking token 数无独立字段,计入 output_tokens(reasoning_tokens 记 0,矩阵已登记);
- 流式 message_delta 翻译帧的 `prompt_tokens` 恒为 0(prompt 分帧在 message_start,事件级无状态无法回填;计量侧不受影响);
- Anthropic 文档区域屏蔽:翻译矩阵按公开稳定契约实现,待可访问后逐条复核。
