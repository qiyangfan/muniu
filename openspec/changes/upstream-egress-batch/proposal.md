# 出站面批次一:桥接工具多轮显式边界 + Gemini 轻形态接入

## Why

2026-09-12 用户点名升格两项此前入 backlog 的出站能力,经架构裁决(见 design)立项:

1. **桥接工具多轮是缺陷级问题**:Anthropic 桥接响应侧 `tool_use` 翻译完整,但请求侧 `role=tool`/`tool_calls` 不翻译——此类请求当前原样透传,让上游吃 400 再以 `upstream_error` 冒出。客户端无从知晓触发了已知限制,失败模式不诚实;这正是「半正确实现比诚实限制更危险」的残余风险面。
2. **Gemini 是被低估的低成本高价值项**:Gemini 官方提供 OpenAI 兼容端点,大概率零新 adapter——config + quirk(以实测偏差为准)+ fixture + 价目即可接入;对个人项目,免费层有直接成本价值。

## What Changes

- **桥接工具多轮显式边界**:含 `role=tool` 或 `assistant.tool_calls` 的请求(多轮中途标记),anthropic 桥接候选获评「不可服务」——多候选别名 failover 跳过该候选(记日志,不计 key EMA 失败);全部候选不可服务时返回 400 `bridge_tool_roundtrip_unsupported` 明确信封。首轮 tools 声明(响应侧翻译已完整)不受影响。
- **Gemini 经官方 OpenAI 兼容端点出站**:provider 配置(既有 openai 兼容 adapter)+ 实测 quirk + conformance fixture + 价目录入;先冒烟实测偏差,quirk 以真上游为准(沿用 Anthropic 文档屏蔽案「真上游才是权威源」的裁决先例)。

## Capabilities

### New Capabilities

- `anthropic-bridge`: 桥接对工具多轮流量的显式边界语义(检测/候选豁免/终态信封/EMA 豁免)。
- `gemini-egress`: Gemini 以 OpenAI 兼容层出站的接入形态与实证纪律。

### Modified Capabilities

(无)

## Impact

- 代码:`internal/proxy`(候选资格判定 + 桥接多轮检测)、`internal/adapter`(quirk)、`conformance/`、`muniu.example.yaml`。
- 文档:`docs/design/upstream-adapters.md`(桥接节:限制登记 → 显式边界契约;供应商矩阵)、`docs/design/traffic-governance.md`(判定表扩「候选适配性豁免」格)、README 适配矩阵。
- 风险面:边界只影响此前已被上游 400 的流量(模糊失败 → 明确失败或换道成功),无存量正确流量回归面。
