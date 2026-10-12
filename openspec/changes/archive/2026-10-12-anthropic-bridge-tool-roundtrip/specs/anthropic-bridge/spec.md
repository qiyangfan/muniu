# anthropic-bridge 变更(anthropic-bridge-tool-roundtrip)

## ADDED Requirements

### Requirement: 工具多轮请求侧翻译

chat→Anthropic 桥接的请求侧翻译必须 (MUST) 覆盖工具多轮上下文:`assistant.tool_calls` 必须翻译为该 assistant 消息内的 `tool_use` 内容块(id/name 原样承载,input 为 arguments 的对象形态,空/非法实参落 `{}`);`role=tool` 消息必须翻译为 user 消息内的 `tool_result` 内容块(`tool_use_id` 回指,`content` 承载结果文本),连续多条 tool 消息必须 (MUST) 合并进同一条 user 消息(满足 Anthropic 交替角色约束);连续同角色的既有文本合并语义必须 (MUST) 升级为块级合并且纯文本对话的出站 wire 必须逐字节不变。`tool_call_id` 在本对话内找不到配对 `tool_calls` 时必须 (MUST) 报错归 bad_request,禁止静默丢弃。`tool_choice` 映射必须 (MUST) 补全 `"none"` 形态(以真上游冒烟实证;上游不接受时回退登记限制并同批修订本条)。

#### Scenario: 工具全轮出站形态

- **WHEN** chat 入站请求含 assistant.tool_calls 与 role=tool 结果消息,经桥接翻译出站
- **THEN** assistant 消息携带 text(如非空)与 tool_use 块(input 为对象形态),其后单条 user 消息按序携带全部 tool_result 块,角色序列满足交替约束

#### Scenario: 连续 tool 消息合并

- **WHEN** 请求在 assistant.tool_calls 后携带两条连续 role=tool 消息
- **THEN** 两条翻译为同一条 user 消息内的两个 tool_result 块,不产生连续 user 消息

#### Scenario: 未配对 tool_call_id 拒绝

- **WHEN** role=tool 消息的 tool_call_id 在本对话任何 assistant.tool_calls 中不存在
- **THEN** 该次出站构造报错归 bad_request,不触达上游

#### Scenario: 纯文本对话 wire 逐字节不变

- **WHEN** 不含任何 tool 上下文的既有对话经桥接翻译出站
- **THEN** 出站 wire 与本变更前逐字节一致(content 为字符串形态,既有 fixture 零改动回放)

#### Scenario: tool_choice none 映射

- **WHEN** 请求携带 `tool_choice:"none"` 经桥接翻译出站
- **THEN** 出站携带 `tool_choice:{"type":"none"}`(真上游冒烟实证接受;拒绝则回退登记不映射并修订本条)

## MODIFIED Requirements

### Requirement: 候选资格豁免与终态信封

翻译路径上检出多轮标记时,豁免只适用于**未声明请求侧工具翻译完整的桥接型**(`ToolRoundtripComplete()` 缺省 false 或如实声明 false;保守语义):多候选别名必须 (MUST) 跳过该候选继续换道(结构化日志留痕,客户端无感);仅该候选可达(或其余候选耗尽)时必须 (MUST) 返回 400,信封 `error.code=bridge_tool_roundtrip_unsupported`,文案说明限制本质与替代路径,含 request_id,不得外泄上游驱动细节。anthropic 桥接已声明翻译完整,携带工具多轮标记的请求必须 (MUST) 经翻译路径正常出站,不再豁免;真实拓扑无豁免实例时豁免机制以测试替身钉住语义,不得删除(未来桥接型的保守缺省依赖它)。同构直通可服务的 anthropic 候选不属于本条豁免对象——其多轮标记流量按直通路径正常服务。

#### Scenario: 多候选别名换道成功

- **WHEN** chat 入站别名候选 [openai 系, 未声明完整的桥接型] 携带工具多轮流量且该桥接候选仅能经翻译路径出站
- **THEN** 该桥接候选被跳过,请求经 openai 系候选服务成功,客户端不感知边界存在

#### Scenario: 桥接独占别名明确拒绝

- **WHEN** chat 入站别名仅该桥接候选可达且请求携带工具多轮标记
- **THEN** 返回 400 `bridge_tool_roundtrip_unsupported` 自解释信封(而非上游 400 转发的 upstream_error)

#### Scenario: anthropic 翻正后工具多轮正常出站

- **WHEN** chat 入站别名仅 anthropic 候选可达且请求携带工具多轮标记
- **THEN** 请求经桥接翻译路径正常出站服务,不豁免、不返回 `bridge_tool_roundtrip_unsupported`

#### Scenario: Messages 入站直通使独占别名恢复服务

- **WHEN** Anthropic Messages 入站别名仅 anthropic 候选可达且请求携带工具多轮标记
- **THEN** 请求经直通路径正常服务,不返回 `bridge_tool_roundtrip_unsupported`
