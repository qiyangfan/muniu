# 流式代理与 Failover 语义(proxy-and-streaming)

> 本文是 muniu 的技术核心与面试叙事中心;文中所有「语义/判定表」为契约,改动须走 ADR。

## 超时模型(三层,各自独立计时)

| 层 | 默认 | 计时起点→终点 | 超时动作 |
|---|---|---|---|
| 拨号 dial | 5s | 发起连接→TLS 握手完成 | 判连接失败,进 failover(可换道) |
| 首 token first_token | 60s | 请求发出→首个**内容帧**到达(非流式=首字节) | 判上游无响应,进 failover(可换道,请求未产生对客户端的任何输出,重放安全) |
| 帧间 chunk_idle | 30s | 上一帧→下一帧 | 判上游僵死;**首 token 后 → 断言出口**(见下) |

配置键 `server.timeouts.*`;三层相互独立,不以一个总超时兜底(长回答不该被总时长误杀)。

## 首 token 边界(本网关最重要的语义定义)

**首 token = 上游 SSE 流中第一个携带实质内容的 delta 事件**(content / reasoning_content / tool_call 增量任一非空)。`role` 帧、空 delta、注释行、usage 终帧均不算。

- **边界之前**:请求对客户端零可见影响,任何失败都可透明换道重放;
- **边界之后**:客户端已收到部分生成内容,**换道重放必然造成内容重复/错乱,禁止**。唯一正确行为:如实断言——发流内错误帧,关流,计量已发生部分。

## Failover 判定表(全格覆盖,测试必须逐格断言)

错误分类 `internal/failover`(纯函数):把「适配器返回的 ErrorClass × 是否已过首 token × 流式与否」映射为动作。

| ErrorClass | 说明 | 未过首 token(流式或非流式) | 已过首 token(流式) |
|---|---|---|---|
| dial_fail / timeout_first | 连不上、握手败、首 token 超时 | **换下一候选**(重放安全) | —(不可能出现) |
| upstream_5xx | 上游 500/502/503 | 换下一候选 | **断言**:流内错误帧+关流 |
| rate_limited_429 | 上游限频 | 该 key 记冷却(语义见 traffic-governance),**立即换下一候选**(不 sleep 等待) | 断言 |
| auth_failed_401/403 | key 无效/无权 | 该 key 记禁用(人工介入级),换下一候选 | 断言 |
| bad_request_400 | 参数被上游拒 | **不换道**直接返回(参数问题换家也会拒;桥接翻译缺陷另行走 adapter 修复) | 断言 |
| mid_stream_fail | 流中断/帧间超时/解析崩 | —(定义上已过首 token) | 断言 |
| context_canceled | 客户端断连 | 静默退出,不换道不计失败 | 静默退出,计量已发生部分 |
| **候选适配性豁免(桥接工具限制)** | 请求含 role=tool / assistant.tool_calls 且候选为桥接型(BridgeKind 标记) | **跳过候选,不计 key EMA/冷却**(适配性不匹配≠上游故障);候选取 key 之前生效 | —(检测在候选循环,先于 attempt) |

候选耗尽仍失败 → 最终错误信封 `code=all_candidates_failed`;候选列表耗尽的 429/401 类错误在信封 `message` 中如实说明「已尝试全部候选」。**全部候选因桥接豁免跳过且无任何真实上游错误** → 最终信封 `code=bridge_tool_roundtrip_unsupported`(400,自解释文案;upstream-egress-batch 契约)。非流式请求没有「已过首 token」态(首字节前全部可重放)。

## SSE 透传实现契约

1. **逐事件转发**:读到完整事件(以空行 `\n\n` 分隔)即 `Write` + `http.Flusher.Flush()`,**禁止攒整包、禁止按行切分 SSE 事件**(多行 data 字段合法存在);半帧残留在缓冲区直到补齐,绝不解析半个事件。
2. 响应头固定:`Content-Type: text/event-stream`、`Cache-Control: no-cache`、`X-Accel-Buffering: no`(防反代缓冲);HTTP 状态在上游响应到达时即确定,**之后不再改写**(Go 已隐式约束)。
3. **断连传导**:客户端断连(`r.Context().Done()`)→ cancel 上游请求 → 上游 Body 立即 EOF → 退出循环;上游多吐的字节不读不计。
4. **流内错误帧**(断言出口唯一输出形态,OpenAI 生态事实标准):

```
data: {"error":{"message":"上游流中断,已生成部分可能不完整","type":"upstream_error","code":"mid_stream_fail","request_id":"01J..."}}

data: [DONE]
```

发完错误帧必须补发 `data: [DONE]` 让客户端干净收尾(与 OpenAI 官方断流行为的差异点,已登记 conformance 备注);非流式的等价形态是正常 JSON 错误信封。
5. quirk 归一化在转发路径**就地完成**(如 DeepSeek `reasoning_content` → 统一 `reasoning` 字段透传给客户端时保留原字段名并附 `muniu_reasoning`?——**否**:归一化策略见 upstream-adapters「字段改写白名单」,默认**不改写客户端可见字段**,只读归一用于计量;改写会让客户端 SDK 产生分家差异,单二进制网关的价值在透明)。

## usage 计量与流式聚合(语义,详见 metering)

- 流式:优先采信上游终止 usage 帧;缺失 → 累计全 delta 文本本地 tiktoken 估算,`estimated=true`。
- 断言中断:按已转发部分估算计费,**不向供应商请款的部分如实标注**(usage 归一表按上游口径)。
- reasoning/cached token 独立列,估算值允许 ±10% 误差(记录 `estimated` 供对账剔除)。

## 性能预算(压测基线,Phase 2 起守)

- SSE 转发单请求增量开销:p99 < 1ms/chunk(基准测试钉住);内存:每请求稳态 ≤ 2× 缓冲(读 32KB + 写 32KB);万级并发连接为 Phase 5 压测目标,不做提前优化但禁止明显反模式(每请求大对象分配、无界缓冲)。

## 测试矩阵(fake 上游剧本 × 判定表全格)

`internal/testkit` 剧本库:`normal`(正常流)/`slow_chunk`(帧间超时触发)/`mid_stream_cut`(流中断)/`half_frame`(半帧+补齐)/`usage_missing`(估算路径)/`stream_429`(冷却+换道)/`stream_5xx_after_first_token`(断言路径)/`client_disconnect`(取消传导)。每个剧本对应至少一个表驱动用例,断言:客户端可见字节序列、usage 落库行、key 池状态变化三项。

## Responses 入站翻译(ADR-0012,契约;fixture 与测试钉死本表)

`POST /v1/responses` 在管道入口前**边缘翻译**为规范 chat 请求(治理/缓存/插件/限流/failover/计量零改动);响应与流式事件反向映射。错误信封沿用 conventions;`not_found` 见模型发现契约。

### 请求映射表

| Responses 入站 | 规范 chat | 约束 |
|---|---|---|
| `model` | `model`(别名) | 必填,路由解析与 chat 同规 |
| `instructions` | system 消息(置于最前) | 与 input 显式 system 并存时 instructions 在前 |
| `input`(string) | 单条 user 消息(content=原文字符串) | 快捷形态 |
| `input[]` type=message | 同 role 消息 | content parts 仅接受 input_text/output_text(文本按序拼接);其余类型 400 `input_item_unsupported` |
| `input[]` type=function_call | assistant 消息带 tool_calls(call_id/name/arguments 直映) | |
| `input[]` type=function_call_output | role=tool 消息(tool_call_id=call_id,content=output) | |
| `tools[]` type=function | chat tools(name/description/parameters 从顶层移入 function) | |
| `tool_choice` | tool_choice | auto/none/required/具名 function 同映射(具名移入 function) |
| `max_output_tokens` | `max_tokens` | |
| `temperature` / `top_p` / `stream` | 同名透传 | |
| `previous_response_id` | **400 `previous_response_id_unsupported`** | 无状态 v1:静默忽略会丢失客户端上下文,属危险失败 |
| `background=true` | **400 `background_unsupported`** | |
| `store` / `metadata` | 忽略(白名单,不转发) | |

### 显式拒绝清单(400,bad_request)

- `previous_response_id_unsupported`:无状态网关不支持服务端会话存储
- `background_unsupported`:不支持后台模式
- `input_item_unsupported`:v1 仅支持文本与函数调用项(如 input_image 拒绝)
- `input_invalid`:input 缺失或结构非法

### 响应与流式事件映射

- 非流式:chat.completion → `{id, object:"response", status:"completed", model, output[], usage}`;`id` = `resp_` + request_id(追责链路一致)。output[]:reasoning_content 存在时 → `{type:"reasoning", text}` 只读项;assistant 文本 → `{type:"message", content:[{type:"output_text"}]}`;tool_calls → `{type:"function_call", name/call_id/arguments}`。
- 流式最小事件集(顺序不变式:created 首帧、completed 终帧、delta 仅出现于两者之间;usage 恒携带,无上游 usage 补零值):
  `response.created` → `response.output_item.added` → (`response.output_text.delta` | `response.function_call_arguments.delta`)* → `response.output_item.done` → `response.completed`(携带 usage)。
  usage 映射:prompt→input_tokens(含 cached details),completion→output_tokens(reasoning 为其上游子集,只入 output_tokens_details.reasoning_tokens,不与 output 相加——加法即客户端可见双计),total=input+output。
- 流中失败(上游 mid_stream_fail 或异常收尾无 [DONE]):发 `response.failed`(携带归一错误信息)后终止——诚实失败,不伪造 completed。
- `GET /v1/responses/{id}`:恒 404 `not_found`、code `response_not_found`(无状态网关不存储 response;与模型发现 404 的 code 区分,避免对账混淆)。

### 与既有子系统的交互

翻译产物与「手工构造的等价 chat 请求」逐字节同构 ⇒ 治理/插件/限流/failover/计量行为等价(钉子测试锁死);语义缓存键取翻译后规范请求 ⇒ **跨协议缓存命中**(等价 chat 先写、Responses 命中)。工具多轮流量经 anthropic 出站受桥接边界契约约束(显式 400/豁免,不静默)。
