# Proposal: anthropic-bridge-tool-roundtrip

## Why

chat 入站 × anthropic 出站的工具多轮是全仓最后一个「登记缺口」:桥接请求侧丢弃 `role=tool`/`assistant.tool_calls`(bridgegate 对工具多轮中途流量适配性豁免,独占时 400 `bridge_tool_roundtrip_unsupported`)。gemini 适配器已在 ADR-0015 实现完整工具往返后,该缺口成为真异构协议「整家实现」故事线上的最后不对称;且 messages 入站经同构直通原生支持工具多轮后,缺口收窄为单一形态,翻译面边界清晰,是收口的最佳时点。

## What Changes

- 桥接请求侧完整翻译:`assistant.tool_calls` → `tool_use` 内容块(id/name/input=args 对象)、`role=tool` → 合并进单条 user 消息的 `tool_result` 内容块(满足 Anthropic 交替角色约束)、连续同角色消息升级为块级合并、`tool_call_id` 配对校验(无配对报错归 bad_request)。
- 纯文本对话 wire 逐字节不变(既有 fixture 零改动回放);内容块形态仅在工具上下文出现。
- `tool_choice` 补 `"none"` → `{"type":"none"}` 映射——以 dashscope anthropic 分支真上游冒烟实证;400 则回退维持登记限制。
- `bridge.ToolRoundtripComplete()` 翻正 `true`;bridgegate 豁免机制降级为防御性保留(testkit 假桥接钉住语义,真实拓扑不再触发)。
- 真上游冒烟:chat 入站工具全轮(声明→tool_calls→tool_result→终答)经 dashscope anthropic 分支实证。

## Capabilities

### Modified Capabilities

- `anthropic-bridge`:工具多轮请求侧翻译完整性要求翻正(声明式缺口关闭);豁免/终态信封要求重划为「未声明完整的桥接型」的防御性语义(真实拓扑 anthropic 翻正后无豁免实例);`tool_choice` 映射表补 none 行(真上游实证后)。
