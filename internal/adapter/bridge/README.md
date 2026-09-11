# Anthropic 桥接翻译矩阵

桥接是唯一允许改写客户端可见字段的适配器(upstream-adapters「字段改写白名单」)。
源契约:Anthropic Messages API(`anthropic-version: 2023-06-01`,公开稳定形态;
官方文档在此网络区域屏蔽,按公开契约保守实现,可访问后逐条复核——HANDOFF 开放决策项)。

## 请求(OpenAI → Anthropic)

| OpenAI 形态 | Anthropic 形态 | 说明 |
|---|---|---|
| `Authorization: Bearer <key>` | `x-api-key: <key>` + `anthropic-version: 2023-06-01` | 鉴权头重写 |
| `messages[role=system/developer]` | 顶层 `system`(多条 `\n\n` 拼接) | system 提升 |
| `messages[user/assistant]` | `messages`(content 折叠为文本) | 连续同角色合并为 `\n\n` 连接(Anthropic 要求交替) |
| `max_tokens` | `max_tokens` | Anthropic 必填;OpenAI 缺省时填 4096 |
| `tools[type=function]` | `tools[{name,description,input_schema}]` | parameters 即 input_schema;字段形态非法时显式报错(归 bad_request),静默丢弃会让上游在无工具状态下作答 |
| `tool_choice`:`auto`/`required`/`{type:function,function:{name}}` | `{type:auto}`/`{type:any}`/`{type:tool,name}` | `"none"` 无精确对应物,不映射(登记限制) |
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

- `stop` 参数未映射(stop_sequences);`tool_choice:"none"` 无精确对应物不映射;图片等多模态 content 丢弃仅保留 text;
- **工具对话往返未翻译**:`role:"tool"` 消息与 assistant 消息的 `tool_calls`(OpenAI 请求侧形态)不进 Anthropic 请求——
  多轮工具对话经桥接不支持,assistant 工具消息折叠为空 content(会被上游拒绝)。流式/非流式的**响应侧** tool_use 翻译不受此限;
- thinking token 数无独立字段,计入 output_tokens(reasoning_tokens 记 0,矩阵已登记);
- 流式 message_delta 翻译帧的 `prompt_tokens` 恒为 0(prompt 分帧在 message_start,事件级无状态无法回填;计量侧不受影响);
- Anthropic 文档区域屏蔽:翻译矩阵按公开稳定契约实现,待可访问后逐条复核。
