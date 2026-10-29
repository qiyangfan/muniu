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
| cost_budget_exceeded | 网关策略:单请求成本预算超限(request-cost-budget) | 静默终结,不换道不计失败(同别名跨候选价目相同,换道无解) | 静默终结(中断出口已自行写流内错误帧/402 信封) |
| **候选适配性豁免(桥接工具限制;防御性保留)** | 请求含 role=tool / assistant.tool_calls 且候选为**未声明工具翻译完整的桥接型**(Bridger ∧ ToolRoundtripComplete=false) | **跳过候选,不计 key EMA/冷却**(适配性不匹配≠上游故障);候选取 key 之前生效 | —(检测在候选循环,先于 attempt;anthropic 桥接已声明完整不再触发,机制经测试替身钉住) |

候选耗尽仍失败 → 最终错误信封 `code=all_candidates_failed`;候选列表耗尽的 429/401 类错误在信封 `message` 中如实说明「已尝试全部候选」。**全部候选因桥接豁免跳过且无任何真实上游错误** → 最终信封 `code=bridge_tool_roundtrip_unsupported`(400,自解释文案;upstream-egress-batch 契约)。非流式请求没有「已过首 token」态(首字节前全部可重放)。

## 请求级成本预算(request-cost-budget 契约)

per-key 单请求成本上界(`max_request_cost_credits`,0=不限),两道闸把成本治理前移到「事中」:

1. **预检闸**(候选循环前,四条入站 lane 共用):预算>0 ∧ 价目可得 ∧ `max_tokens`>0 时,估算「输入成本 + max_tokens×输出价」上界(缓存折扣不计,保守不低估),超界 → 402 `cost_budget_exceeded`,**上游零调用、不落计量行**(与 strict 治理 400 同族)。`max_tokens` 缺失 = 输出上界不可知,预检不拒绝(不伪保护);无价目别名同样旁路。
2. **流中闸**(翻译出站路径):按累计成本逐帧检查——输入/输出各取「usage 帧真值优先、逐帧估算兜底」(逐帧估算轻微保守高估,预算宁紧勿松);终态帧不拦(响应已完成,事后无中断意义)。已开闸 → 流内错误帧(`cost_budget_exceeded`,402 族)+ 干净收尾,已收部分保留;未开闸 → 402 信封。已发生部分如实计量落账(`error_class=cost_budget_exceeded`)。**中断不计 key 失败**(不冷却不禁用不记 EMA,判定表静默格)。
3. **诚实边界**:非流式无中断闸(上游原子生成,中断只省传输不省账单,不做伪保护);直通 passthrough 流已开闸段不中断(无原生协议错误帧合成机制,未开闸仍可拦);embeddings 端点不在闸覆盖内(输出形态不同)。三者均为登记限制,非缺陷。

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
| `input[]` message content=字符串 | 同 role 消息(content=原文字符串,折叠为单 text 部件) | 官方规范快捷形态,与部件数组等价;canonical 逐字节同构(对拍钉死) |
| `input[]` type 省略+携带 role | 缺省判定为 message(官方 SDK 缺省产物即该形态) | 缺省仅此一例;无 type 无 role 400 `input_invalid`,function_call 家族恒带 type 不猜测 |
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

### 知识注记:百炼 responses 原生端点多轮回放缺口(2026-10-11 实证,防误判 muniu 入站)

百炼(dashscope)`/api/v2/services/agents` 系 responses 原生端点对多轮工具回放存在**服务端缺口**:请求携带 role=tool 历史条目(官方 SDK 标准回放形态)时返回 400 校验错(同轮四路径对照实证,非 muniu 翻译层问题)。排查期间的 kongming 侧绕行(手工构造显式部件数组形态)在修复后**无须回滚**——显式形态对 OpenAI 官方/百炼/muniu 三方兼容面最宽,仍是正确写法。若客户端经 muniu 走百炼 responses 出现多轮 400,先核对该上游缺口再怀疑网关翻译。

## Anthropic 入站翻译(ADR-0013,契约;测试钉死本表)

`POST /v1/messages` 与 Responses 入站同款**边缘翻译**架构:入站请求在管道入口前一次性翻译为规范 chat 请求,复用既有管道(治理/缓存/插件/限流/failover/计量零改动)。Anthropic 协议客户端(Claude Code 等)由此直连 muniu,出站侧经 OpenAI 兼容家或各家 Anthropic 分支端点(docs/provider-presets.md 表 B)任意换道。

### 鉴权头适配

数据面 verifier 只认 `Authorization: Bearer`;Anthropic 客户端以 `x-api-key` 或 `Authorization` 携带 key。装配层 `server.AdaptAnthropicKey` 仅对 `/v1/messages` 且缺失 Authorization 时把 x-api-key 补写为 Bearer(先于 verifier),其余请求零改动。

### 请求映射表

| Anthropic 入站 | 规范 chat | 约束 |
|---|---|---|
| `model` | `model`(别名) | 必填,路由解析与 chat 同规 |
| `max_tokens` | `max_tokens` | Anthropic 必填;缺失时省略(不伪造缺省值) |
| `system`(string \| text 块数组) | system 消息(置于最前) | 多块换行合并;非 text 块 400 |
| `messages[].content`(string) | 同 role 消息 | 快捷形态 |
| text 块 | 文本按序换行合并 | |
| thinking 块 | **丢弃** | 请求侧签名回传语义不成立,见丢弃清单 |
| tool_use 块 | assistant `tool_calls`(id/name 直映,input → arguments JSON 字符串) | 混合文本块时拆为 assistant(text)+assistant(tool_calls) 两条 |
| tool_result 块 | role=tool 消息(tool_use_id → tool_call_id) | content 字符串或 text 块数组;多条各自一条 |
| `tools[]`(name/description/input_schema) | chat tools(input_schema → parameters) | |
| `tool_choice` | tool_choice | auto/none 直通;any→required;具名 tool→function 嵌套 |
| `temperature` / `top_p` / `stream` | 同名透传 | |
| `stop_sequences` | `stop` | |
| `top_k` / `metadata` | 忽略(白名单,不转发) | chat 形态无对应语义,静默丢弃 |

### 显式拒绝与丢弃清单(400,Anthropic error JSON)

- `content_block_unsupported`:v1 仅支持 text/tool_use/tool_result 块(image/document 等多模态拒绝)
- `input_invalid`:model 缺失、messages 空、角色非法或结构非法
- 丢弃(不拒绝):thinking 块(签名回传语义不成立)、top_k、metadata
- 错误形态:翻译期失败以 Anthropic error JSON(`{"type":"error","error":{type,message}}`)返回;**管道内错误(流式 finish 改写)按类型映射 Anthropic 原生词汇表**——`bad_request→invalid_request_error`、`auth_failed→authentication_error`、`forbidden→permission_error`、`not_found→not_found_error`、`rate_limited→rate_limit_error`,无对应物(quota_exceeded/timeout/upstream_error/internal)维持 `api_error` 兜底由 HTTP 状态码承载语义(2026-10-14 裁决:同一 lane 一套错误形态,原「待真实消费者」登记条款由 Anthropic 协议 SDK 实际消费满足)

### 响应与流式事件映射

- 非流式:chat.completion → `{id:"msg_"+request_id, type:"message", role:"assistant", model, content[], stop_reason, stop_sequence:null, usage}`。content[]:reasoning_content → `{type:"thinking"}`(**无签名的展示形态**);文本 → `{type:"text"}`;tool_calls → `{type:"tool_use", id/name, input=arguments 解析}`。stop_reason 映射:stop→end_turn、length→max_tokens、tool_calls→tool_use、其余 null。usage:prompt→input_tokens、completion→output_tokens、cached→cache_read_input_tokens(reasoning 为 completion 子集不另立)。
- 流式(顺序不变式:message_start 首帧、message_stop 终帧、content_block_* 仅出现于两者之间;块切换先 content_block_stop;块索引按发出顺序递增):
  `message_start` → [`content_block_start` → `content_block_delta`(text_delta \| thinking_delta \| input_json_delta)* → `content_block_stop`]* → `message_delta`(stop_reason + usage)→ `message_stop`。
  **已知偏差(登记)**:`message_start.usage.input_tokens` 恒 0(上游 usage 流尾才知),真实用量在 `message_delta.usage` 补全;SSE 用 data-only 帧(事件 type 在 JSON 内,无 `event:` 行,SDK 兼容)。
- 流中失败(上游 mid_stream_fail 或无 [DONE] 收尾):发 `error` 事件后终止——诚实失败,不伪造 message_stop;管道在流开始前失败的信封 body 改写为 Anthropic error JSON(状态码不变)。

### 与既有子系统的交互

翻译产物与「手工构造的等价 chat 请求」逐字节同构 ⇒ 治理/插件/限流/failover/计量行为等价(钉子测试锁死);语义缓存键取翻译后规范请求 ⇒ 跨协议缓存命中。**chat 入站 × anthropic 出站的工具多轮已完整翻译**(anthropic-bridge-tool-roundtrip:tool_calls→tool_use 块、role=tool→合并 tool_result、配对校验;纯文本 wire 逐字节不变),真异构两家(anthropic/gemini)均整家实现;豁免机制降级为「未声明完整的桥接型」的防御性语义(测试替身钉住);Anthropic Messages 入站不受任何影响(直通零翻译)。

## 同构直通(ADR-0014,契约;组合测试冻结本节)

入站协议与候选适配器原生协议一致时,该次尝试走**直通**:出站 wire = 客户端原始请求体(仅 model 字段原位手术),响应/流式/错误信封原样透传。翻译只属于跨协议对——协议平等,chat 非特权 wire 形态。

### 路径判定表(每 attempt;判定点唯一)

| 入站 | 候选 | 路径 | 声明来源 |
|---|---|---|---|
| chat | 任意 | 翻译(基座透传) | —(无直通增益) |
| messages | anthropic 适配器 | **直通** | 适配器类型即声明(`NativeProto` 协议身份自述) |
| messages | 其他适配器 | 翻译 | 协议身份不符 |
| responses | `native_responses: true` | **直通** | provider 显式声明(零自动探测) |
| responses | 未声明 | 翻译兜底 | 「OpenAI 兼容」≠「responses 端点可用」 |

回落信号(该 attempt 放弃直通改翻译,不计失败不动 key 健康度):①治理对该候选实际干预(直通 attempt 以**原始体**评估治理——翻译器静默丢弃的参数不在 canonical,治理对 wire 的裁决权必须覆盖它们);②插件改写 canonical(原始体过期)。

### 保真不变式与错误信封分域

- 直通出站除 model 值字节外**逐字节**等于客户端原始体(`RewriteModelInPlace` 原位手术,禁 map 往返重排键序);响应状态/头/体(流式逐帧)原样透传。重复顶层 `model` 键收敛为单键(首处换值、其余整段摘除,仅触及 model 字段字节)——真实上游对重复键均拒绝(百炼兼容模式报缺参、deepseek 报 duplicate field,2026-10-30 真端点实证),只改末键会恰好产出该形态。
- **错误信封按路径分域**:直通=上游信封原样到达(不换道类,含流内失败帧诚实透传后终止,不合成任何非上游形态);翻译=既有归一信封。两套契约并存,禁顺手统一。
- writer 模式位(直通=恒等写/翻译=反向映射)仅首字节提交前可切换;已提交绝不换道/二次写出/混用形态(`tw.committed` 既有不变式)。

### 惰性拒绝(失格标记)

翻译失格(image 等)不再一律边缘 400:别名含直通候选 → 放行直通(多模态原生支持);换道落翻译候选 → 出站前升格既有 400 信封(`content_block_unsupported`/`input_item_unsupported`,文案不变);无直通候选别名 → 维持现状即时 400(逐字节一致)。失格请求跳过语义缓存检索与入库、不影响 key 健康度。

### 语义缓存双存

直通成功入库为双存:canonical chat 形态(跨协议命中,既有等价性不变)+ 原生响应体+协议标签+表单标记(stream|nonstream)。同构命中且表单兼容 → 原生恒等回放(零翻译);表单不兼容/跨协议 → canonical 反向映射;canonical 衍生失败仅降级为跨协议不可命中。

### 有状态粘性路由(原生 responses lane)

- `previous_response_id` 仅由原生 lane 服务:别名无原生候选 → 400 `previous_response_id_unsupported`(翻译路径维持既有拒绝);亲和未知 → 400 `response_affinity_lost` 自解释(禁盲发);亲和命中 → 候选域收缩至亲和 provider(域内 key/候选 failover 照常,跨 provider 不换道——服务端状态定向单上游的显式语义)。
- 亲和表:id→provider→**归属 key**(创建请求的鉴权 key 标识,responses-state-ownership),进程内 TTL(24h)+LRU(4096);成功响应 id 回写;集群共享后端按 ADR-0008 复用(未配共享=实例内粘性,登记;归属字段随共享视图同步演进)。指标 `muniu_sticky_requests_total{result=hit|lost|unsupported}`(归属不匹配计入 lost——对运维即丢失)。
- `GET/DELETE /v1/responses/{id}`:亲和已知**且归属匹配**(记录 key = 请求鉴权 key;无鉴权信息 fail-closed)→ 按动词代理转发亲和 provider 原样返回;亲和未知**或归属不匹配**→ 恒 404 `response_not_found`(两类不可区分,不泄露存在性),归属不匹配时上游零调用——DELETE 不可逆,绝不出站删他 key 状态。粘性 prevID 归属不匹配 → 与「亲和未知」不可区分的 `response_affinity_lost` 降级。重启丢失 → 自解释降级(提示改发全量 input)。
- 状态请求旁路语义缓存(检索与入库皆零,限流计量照常)。

## Gemini 入站翻译(ADR-0015,契约;conformance/gemini 语料钉死本表)

`POST /v1beta/models/{model}:generateContent`(及 `:streamGenerateContent?alt=sse`)与 Responses/Messages 入站同款**边缘翻译**:模型别名取自 URL 路径(Gemini wire 无 model 字段;流式语义在端点本身,canonical 注入 `stream:true`)。鉴权适配 `server.AdaptGeminiKey`(x-goog-api-key 头 / ?key= 参数 → Bearer;?key= 读后剥离)。

### 请求映射表

| Gemini 入站 | 规范 chat | 约束 |
|---|---|---|
| 路径 `{model}` | `model`(别名) | 别名不存在与 chat `model_not_found` 同规(信封 Gemini 形态) |
| `contents[].role` user/model | user/assistant | 未知 role 400 `input_invalid` |
| `parts[].text` | 文本拼接(多部件换行合并) | thought 请求部件丢弃(签名回传语义不成立) |
| `parts[].inlineData`(image/*) | content 数组 `image_url`(data URL 合成) | 音频/视频 400 `content_part_unsupported` |
| `parts[].fileData` | **400 `content_part_unsupported`** | |
| `parts[].functionCall`(model) | assistant tool_calls(id 按名合成,同名按序去重) | |
| `parts[].functionResponse`(user) | role=tool 消息(tool_call_id 回指同名最近 call) | response 先于 call 400 `input_invalid` |
| `systemInstruction`(字符串/parts 块) | system 消息 | |
| `tools[].functionDeclarations` | chat tools(parameters 直映) | |
| `toolConfig.functionCallingConfig.mode` | tool_choice:AUTO→auto / ANY→required / ANY+单元素名单→具名 / **NONE→none** | 非法 mode 400;多元素名单静默丢弃(登记) |
| `generationConfig` temperature/topP/topK/maxOutputTokens/stopSequences | 同义映射(topK→canonical `top_k`,与 top_p 同缝) | `safetySettings`/`candidateCount`/`seed` 静默丢弃(登记;canonical chat 无承载字段) |

### 响应与流式映射

- 非流式:chat.completion → GenerateContentResponse(candidates[].content.parts:reasoning_content→`thought:true` 部件、text、tool_calls→functionCall[args 对象];finishReason:stop/tool_calls→STOP、length→MAX_TOKENS、content_filter→SAFETY;usageMetadata:prompt/candidates/cached/thoughts=reasoning[子集口径];modelVersion 回显别名)。
- 流式(alt=sse):OpenAI chunk → 增量帧(text/thought parts)→ 终帧(finishReason+usageMetadata);functionCall 按收束帧发出(完整对象,无增量形态,登记);上游断流以 `{error:{code:500,status:"INTERNAL"}}` 帧诚实收尾,不伪造正常终帧。
- 错误信封:`{error:{code,message,status}}`(+`muniu_code` 网关机器码扩展字段,SDK 容忍附加字段);status 映射 bad_request→INVALID_ARGUMENT、429→RESOURCE_EXHAUSTED、超时→DEADLINE_EXCEEDED、其余→INTERNAL。上游「HTTP 200 + 流内错误帧」失真样式已入语料负面形态(联调断言口径=终帧内容而非状态码)。

### 管道等价性

翻译产物与等价 chat 请求 canonical 逐字节同构 ⇒ 治理/插件/限流/failover/计量/语义缓存全同构(跨协议缓存命中钉子测试);gemini 候选工具多轮翻译完整,不触发桥接豁免(声明式缺口,ADR-0015 决策 4)。

## 同步 images 通道（ADR-0016）

独立 handler 挂载于既有数据面鉴权/限流/配额链，插件请求体钩子在候选循环前执行。OpenAI 兼容基座声明 `ImagePreparer`，只改模型名；不具备能力的适配器返回 400 `images_not_supported`，不换道。沿用非流式 failover 判定表，5xx/429/传输错误可换候选；请求体 4MiB、响应体 64MiB，超限或中断不交付半包。响应读取在同一 FirstToken 计时器内完成，请求取消传导至出站。全请求仅加载一次拓扑与价格快照。

只支持同步 generations；异步 submit/poll、edits/variations、audio 不在此通道。上游 url 原样返回，不托管图片、不注入 response_format。断流候选 cost=0，可能发生的上游生成费用为诚实边界，见 metering.md。
