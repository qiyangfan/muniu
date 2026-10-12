# Spec Delta

## MODIFIED Requirements

### Requirement: 多轮标记检测

翻译路径必须在候选出站前检测工具多轮标记:请求消息含 `role=tool`,或任一 assistant 消息带非空 `tool_calls`。仅声明 `tools` 参数(无中途标记)的请求必须 (MUST) NOT 触发边界。检测对象是该次尝试的实际出站路径而非候选适配器类型:同构直通服务(Anthropic Messages 入站 × anthropic 适配器候选,协议原生 passthrough)不经过翻译,必须 (MUST) NOT 触发本边界;仅当该次尝试将经 chat→Anthropic 桥接翻译出站时才适用。

#### Scenario: 首轮 tools 声明放行

- **WHEN** 请求声明 tools 但消息中无 role=tool / assistant.tool_calls
- **THEN** 桥接候选正常出站(响应侧 tool_use 翻译照常),行为与现状一致

#### Scenario: 两类中途标记均检出

- **WHEN** 翻译路径上分别携带 role=tool 消息、assistant.tool_calls 的请求到达桥接候选
- **THEN** 两类均判定为多轮中途,进入豁免/拒绝语义

#### Scenario: Messages 入站直通不受边界约束

- **WHEN** Anthropic Messages 入站携带工具多轮上下文(role=tool / assistant.tool_calls),别名候选为 anthropic 适配器
- **THEN** 该候选走直通路径正常出站(原始体零翻译),不触发豁免/拒绝语义;Anthropic 客户端工具多轮对话经 anthropic 上游全链可用

### Requirement: 候选资格豁免与终态信封

翻译路径上检出多轮标记时,桥接候选必须 (MUST) 按适配性豁免处理:多候选别名必须 (MUST) 跳过桥接候选继续换道(结构化日志留痕,客户端无感);仅桥接可达(或其余候选耗尽)时必须 (MUST) 返回 400,信封 `error.code=bridge_tool_roundtrip_unsupported`,文案说明限制本质与替代路径,含 request_id,不得外泄上游驱动细节。同构直通可服务的 anthropic 候选不属于本条豁免对象——其多轮标记流量按直通路径正常服务。

#### Scenario: 多候选别名换道成功

- **WHEN** chat 入站别名候选 [deepseek, anthropic-桥接] 携带工具多轮流量且 anthropic 候选仅能经翻译路径出站
- **THEN** 桥接候选被跳过,请求经 deepseek 服务成功,客户端不感知边界存在

#### Scenario: 桥接独占别名明确拒绝

- **WHEN** chat 入站别名仅桥接候选可达且请求携带工具多轮标记
- **THEN** 返回 400 `bridge_tool_roundtrip_unsupported` 自解释信封(而非上游 400 转发的 upstream_error)

#### Scenario: Messages 入站直通使独占别名恢复服务

- **WHEN** Anthropic Messages 入站别名仅 anthropic 候选可达且请求携带工具多轮标记
- **THEN** 请求经直通路径正常服务,不返回 `bridge_tool_roundtrip_unsupported`
