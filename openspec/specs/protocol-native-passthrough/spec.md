# protocol-native-passthrough Specification

## Purpose
同构协议直通路由:入站协议与候选的原生协议一致时,出站使用客户端原始请求体、响应原样返回,消除同构流量的双重翻译保真损失;翻译只属于跨协议对——协议平等,chat 非特权 wire 形态,canonical 仅作控制面内部键。控制面(治理/缓存/限流/计量/插件)语义不变。

## Requirements

### Requirement: 同构直通路径判定

入站协议与候选的原生协议一致时,该次尝试必须 (MUST) 走直通路径:出站请求体为客户端原始字节;响应原样透传。v1 范围:①Anthropic Messages 入站 × `adapter: anthropic` 候选;②Responses 入站 × 显式声明 `native_responses` 能力的候选(声明制,零自动探测——「OpenAI 兼容」不等于「responses 端点可用」,未声明候选维持既有 chat 翻译兜底)。路径判定必须 (MUST) 为每次尝试级——failover 换道后对新候选重新判定;入站协议与候选原生协议不一致时(如换道到未声明候选),必须 (MUST) 走既有翻译路径。路径判定必须 (MUST) 由显式声明派生(入站路由 + 适配器类型/能力声明),禁止 (MUST NOT) 对请求体做协议自动探测。

#### Scenario: 同构流量直通服务

- **WHEN** Claude Code 以 `POST /v1/messages` 发送携带 image 内容块、thinking 回传、cache_control、stop_sequences 的请求,别名候选为 `adapter: anthropic` 的 provider
- **THEN** 请求不触发 `content_block_unsupported` 等翻译期拒绝,上游收到除 model 字段外逐字节等于客户端原始体的请求,行为与客户端直连该上游一致

#### Scenario: Responses 声明直通生效

- **WHEN** Responses 入站请求(含 input_image 部件)发往声明 `native_responses: true` 的候选
- **THEN** 该候选走直通路径,上游收到除 model 字段外逐字节等于客户端原始体的请求,图像部件原生透传

#### Scenario: 未声明候选维持翻译兜底

- **WHEN** 同类 Responses 请求发往未声明 `native_responses` 的 openai 系候选
- **THEN** 维持既有 chat 翻译路径,行为与本变更前逐字节一致(image 部件按既有翻译语义拒绝)

#### Scenario: 换道跨协议边界回落翻译路径

- **WHEN** 同一请求的主候选(anthropic)返回可换道错误,failover 切换到 openai 系候选
- **THEN** 第二候选按既有翻译路径服务(canonical chat 出站、响应反向映射为 Anthropic 形态),客户端获得合法完整响应

### Requirement: 直通 wire 保真不变式

直通路径的出站请求体除 model 字段外必须 (MUST) 逐字节等于客户端原始请求体;响应侧状态码、响应头、响应体(非流式与流式逐帧)必须 (MUST) 原样透传,零字段增删改;上游错误信封必须 (MUST) 原样到达客户端(不经过翻译路径的信封归一改写)。鉴权头注入与目标 URL 拼接按候选适配器的既有声明执行。

#### Scenario: 多模态与扩展字段零丢失

- **WHEN** 直通路径服务携带 image 块、`top_k`、`metadata`、`stop_sequences`、`tool_choice` 任意合法取值的请求
- **THEN** 上游收到的请求体中上述字段与客户端发送逐字节一致(翻译路径的丢弃/拒绝行为不适用)

#### Scenario: 流式事件保真含真实首帧 usage

- **WHEN** 直通路径服务流式请求且上游按协议发送 `message_start`(含真实 `usage.input_tokens`)
- **THEN** 客户端收到的事件序列与上游逐字节一致,`message_start.usage.input_tokens` 为上游真实值(翻译路径登记的恒 0 偏差不出现)

#### Scenario: 上游错误原样透传

- **WHEN** 直通路径上游返回 4xx 错误 JSON 且该错误类判定为不换道
- **THEN** 客户端收到的错误体与上游响应逐字节一致

#### Scenario: Responses 对象与事件流原样透传

- **WHEN** 直通路径服务 Responses 请求(非流式对象与 SSE 事件流两形态,状态与无状态请求均含)
- **THEN** 客户端收到的响应与上游逐字节一致(含上游原生 usage 形态);状态请求的粘性语义见「有状态粘性路由」

### Requirement: 控制面等价性

直通路径必须 (MUST) 与翻译路径共享全部控制面语义:鉴权、配额、RPM/TPM、语义缓存指纹(取边缘翻译后的 canonical 请求)、计量落账口径(usage 从上游原生形态解析)、插件请求体钩子(作用于 canonical 请求)。直通必须 (MUST NOT) 改变任一控制面的判定结果或落账字段。缓存条目必须 (MUST) 双存:canonical 形态(跨协议命中)与直通原始响应体 + 入站协议标签 + 表单标记(同构命中);同构命中且表单兼容时必须 (MUST) 优先回放原生体(恒等,零翻译),表单不兼容或跨协议命中回退 canonical 路径。

#### Scenario: 计量口径跨路径一致

- **WHEN** 同一语义请求分别经直通路径(anthropic 候选)与翻译路径(openai 系候选)成功
- **THEN** 两条 usage_logs 落账行的口径一致(prompt/completion/cached tokens 字段语义相同,各自为对应上游真实用量)

#### Scenario: 缓存跨路径互通

- **WHEN** 直通路径成功的响应入库后,同一别名收到等价 chat 请求
- **THEN** 该 chat 请求命中同一缓存条目(canonical 形态可跨协议命中)

#### Scenario: 同构命中原生回放

- **WHEN** 直通(anthropic 候选)成功的流式响应入库后,同别名收到同构 messages 请求且表单兼容(流式对流式)
- **THEN** 回放直通原始响应体(逐字节等于上游原生形态),不经 canonical 反向映射

### Requirement: 有状态粘性路由

携带 `previous_response_id` 的请求必须 (MUST) 仅由原生 lane 服务:别名不含原生 responses 候选时必须 (MUST) 返回 400 `previous_response_id_unsupported` 自解释信封(翻译路径维持既有拒绝,chat 上游无服务端状态)。原生 lane 上必须 (MUST) 经亲和表路由:成功响应的 id 记录 id→provider(进程内 TTL + LRU 容量上限,集群形态复用共享后端);状态请求的候选域必须 (MUST) 收缩至亲和 provider——域内 key/候选 failover 照常,跨 provider 换道不发生(服务端状态定向单上游的显式语义);亲和未知(网关重启丢失/非本网关创建)时必须 (MUST) 返回自解释错误,禁止 (MUST NOT) 盲发上游。状态请求必须 (MUST) 旁路语义缓存检索与入库(上下文在服务端,指纹无意义)。粘性请求数与亲和命中率必须 (MUST) 进指标可观测。

#### Scenario: 粘性路由与域内故障转移

- **WHEN** 携带 `previous_response_id` 的请求命中亲和 provider,其首选 key 处于冷却
- **THEN** 在该 provider 域内取可用 key/候选服务成功,不跨 provider 换道

#### Scenario: 亲和未知自解释

- **WHEN** `previous_response_id` 无亲和记录(网关重启后或非本网关创建)
- **THEN** 返回自解释错误(说明粘性状态丢失,建议改发全量条目),不盲发上游引发 404 换道级联

#### Scenario: 状态请求旁路语义缓存

- **WHEN** 携带 `previous_response_id` 的请求到达
- **THEN** 零缓存检索、零缓存入库(无论命中与否),计量与限流照常

### Requirement: 不可翻译内容的惰性拒绝

边缘翻译遇不可翻译内容时必须 (MUST) NOT 一律立即以 400 终结请求:别名存在直通资格候选时,该请求必须 (MUST) 先按直通路径服务;仅当某次尝试需要翻译路径出站时,必须 (MUST) 在该尝试出站前升格为该入站协议的既有 400 信封(messages 为 `content_block_unsupported`、responses 为 `input_item_unsupported`,文案不变)。别名不含任何直通资格候选时必须 (MUST) 保持现状即时 400(行为逐字节一致)。翻译失格的请求必须 (MUST) 跳过语义缓存检索与入库,必须 (MUST) NOT 因失格被记为失败或影响 key 健康度。

#### Scenario: 纯直通别名多模态正常服务

- **WHEN** 携带 image 内容块的 messages 请求发往仅含 anthropic 候选的别名
- **THEN** 请求经直通路径正常服务(不返回 `content_block_unsupported`),行为与客户端直连上游一致

#### Scenario: Responses 失格经直通恢复、换道升格

- **WHEN** 携带 input_image 部件的 Responses 请求发往候选 [声明 native_responses 的候选, chat 翻译候选],声明候选返回可换道错误
- **THEN** 切换翻译候选前升格返回 400 `input_item_unsupported`(不触达该上游);若声明候选成功则图像原生服务

#### Scenario: 无直通别名维持即时 400

- **WHEN** 携带 image 内容块的 messages 请求发往仅含 openai 系候选的别名
- **THEN** 在进入管道前即时返回 400 `content_block_unsupported`,行为与本变更前逐字节一致

#### Scenario: 换道落翻译候选时升格 400

- **WHEN** 携带 image 内容块的请求发往候选 [anthropic, deepseek],anthropic 候选返回可换道错误
- **THEN** failover 切到 deepseek 前升格返回 400 `content_block_unsupported`(翻译路径对该请求不可服务,不触达 deepseek 上游)

#### Scenario: 失格请求不进语义缓存

- **WHEN** 翻译失格的请求经直通路径成功返回
- **THEN** 该请求零缓存检索、零缓存入库,后续等价 chat 请求不命中任何由此产生的条目

### Requirement: 直通回落纪律

存在下列情形之一时,该次尝试必须 (MUST) 放弃直通并回落翻译路径,行为与本变更前逐字节一致:①能力目录对目标候选声明约束且参数治理实际产生干预;②插件改写了 canonical 请求体(直通原始体相对 canonical 已过期)。回落必须 (MUST) NOT 记为失败、必须 (MUST) NOT 影响 key 健康度,必须 (MUST) 以结构化日志留痕。

#### Scenario: 治理干预触发回落

- **WHEN** 能力目录对目标候选声明参数约束且请求携带越界参数
- **THEN** 该次尝试走翻译路径,治理干预触达出站 wire(与翻译路径行为一致),响应正常返回

#### Scenario: 插件改写触发回落

- **WHEN** 插件钩子改写了 canonical 请求体后请求继续
- **THEN** 该请求全程走翻译路径,插件改写内容到达上游,直通不使用过期原始体

### Requirement: 响应路径切换不变式

响应 writer 的直通/翻译模式切换必须 (MUST) 仅发生在首字节提交前;响应已提交后必须 (MUST) 遵循既有不变式——绝不换道、绝不二次写出、绝不混用两种形态。非流式缓冲与流式逐帧两种形态均适用。

#### Scenario: 首字节前失败换道切换模式

- **WHEN** 直通主候选在产出任何字节前返回可换道错误
- **THEN** failover 切换下一候选并以翻译路径服务,客户端只见单一完整响应

#### Scenario: 已提交流不换道

- **WHEN** 直通主候选流式响应已开始后上游断流
- **THEN** 不换道、不重写,客户端收到的行为与本变更前已提交流的既有契约一致

### Requirement: 状态 lane 读删代理

`GET /v1/responses/{id}` 与 `DELETE /v1/responses/{id}` 必须 (MUST) 按亲和分域:亲和已知 → 以对应 HTTP 动词代理转发亲和 provider,状态码/响应头/体零改写原样返回;亲和未知 → 恒 404 `response_not_found` 自解释信封(网关不存储 response,不因曾经代理过而可查)。出站构造按原生 responses 端点声明(鉴权头注入同创建路径),取 key 失败返回 429 类自解释信封,不得盲发。

#### Scenario: 亲和已知 GET 代理转发

- **WHEN** 亲和表中存在该 response id 且客户端 GET
- **THEN** 以 GET 代理亲和 provider 原样返回上游响应

#### Scenario: 亲和已知 DELETE 代理转发

- **WHEN** 亲和表中存在该 response id 且客户端 DELETE
- **THEN** 以 DELETE 代理亲和 provider 原样返回上游响应(上游执行状态删除)

#### Scenario: 亲和未知恒 404

- **WHEN** 该 response id 无亲和记录(重启丢失/非本网关创建),GET 或 DELETE 到达
- **THEN** 返回 404 `response_not_found` 自解释信封,不触达上游
