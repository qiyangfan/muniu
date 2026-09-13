# anthropic-bridge Specification

## Purpose
Anthropic 桥接对工具多轮流量的显式边界:多轮中途标记(请求含 `role=tool` 或 assistant 带 `tool_calls`)的请求,桥接候选按适配性豁免处理——多候选别名换道跳过,仅桥接可达时返回自解释终态信封。消除「原样透传让上游 400 再以 upstream_error 冒出」的不诚实失败模式;首轮 tools 声明不受影响(响应侧翻译完整)。

## Requirements

### Requirement: 多轮标记检测

桥接必须在候选出站前检测工具多轮标记:请求消息含 `role=tool`,或任一 assistant 消息带非空 `tool_calls`。仅声明 `tools` 参数(无中途标记)的请求必须 (MUST) NOT 触发边界。

#### Scenario: 首轮 tools 声明放行

- **WHEN** 请求声明 tools 但消息中无 role=tool / assistant.tool_calls
- **THEN** 桥接候选正常出站(响应侧 tool_use 翻译照常),行为与现状一致

#### Scenario: 两类中途标记均检出

- **WHEN** 分别携带 role=tool 消息、assistant.tool_calls 的请求到达桥接候选
- **THEN** 两类均判定为多轮中途,进入豁免/拒绝语义

### Requirement: 候选资格豁免与终态信封

检出多轮标记时,桥接候选必须 (MUST) 按适配性豁免处理:多候选别名必须 (MUST) 跳过桥接候选继续换道(结构化日志留痕,客户端无感);仅桥接可达(或其余候选耗尽)时必须 (MUST) 返回 400,信封 `error.code=bridge_tool_roundtrip_unsupported`,文案说明限制本质与替代路径,含 request_id,不得外泄上游驱动细节。

#### Scenario: 多候选别名换道成功

- **WHEN** 别名候选 [deepseek, anthropic-桥接] 携带工具多轮流量
- **THEN** 桥接候选被跳过,请求经 deepseek 服务成功,客户端不感知边界存在

#### Scenario: 桥接独占别名明确拒绝

- **WHEN** 别名仅桥接候选可达且请求携带工具多轮标记
- **THEN** 返回 400 `bridge_tool_roundtrip_unsupported` 自解释信封(而非上游 400 转发的 upstream_error)

### Requirement: 豁免不计入上游故障

适配性豁免必须 (MUST) NOT 计入 key EMA 失败、必须 (MUST) NOT 触发冷却——适配性不匹配不是上游故障(与 adapter_missing 早退、strict 治理拒绝同一裁决口径);failover 判定表扩「候选适配性豁免」格并留契约测试。

#### Scenario: 豁免无健康度副作用

- **WHEN** 桥接候选因多轮标记被连续豁免
- **THEN** 该候选 key 的 EMA 健康度与冷却状态零变化,后续非工具流量照常经桥接出站
