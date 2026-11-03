# Proposal

## Why

`/v1/responses` 入站流式翻译路径不透出上游 `reasoning_content` 增量(R1 类客户端可见空白,验收记录 57 遗留挂账):非流式已透出 reasoning 输出项(responses.go 冻结形态)、chat 入站基座字节透传恒通,唯独流式翻译丢推理文本——同一上游流式/非流式可见性不一致,违反能力完备性(owner 2026-11-03 插话裁决为正常能力需求,「等要求」话术废止前例重申)。

## What Changes

- `ResponsesStreamEncoder` 新增 reasoning 状态机:上游 `delta.reasoning_content` 增量 → 官方事件词汇 `response.output_item.added(reasoning)` → `response.reasoning_summary_text.delta`(逐增量)→ `response.reasoning_summary_text.done` → `response.output_item.done`。item done 形态与非流式冻结契约同形(`type=reasoning`+`text` 原文)。推理与正文/工具混流时先收束当前 item 再开新 item。
- 语义口径登记:官方该事件族语义为 reasoning **summary**,本网关承载**原文透传**——词汇借官方(客户端已认识),内容语义放宽,design 记录差异。
- usage 口径不变(reasoning_tokens 已含于 completion,明细单列,不重复计数)。
- 非流式/chat/messages/embeddings/images 零触碰。

## Capabilities

### New Capabilities

(无)

### Modified Capabilities

- `responses-inbound`:新增「流式 reasoning 透传」requirement(事件序列、item 形态、混流收束顺序)。

## Impact

- `internal/protocol/responses.go`:encoder 三字段+reasoning 状态机(~60 行)。
- `internal/protocol/responses_test.go`:事件序列契约测试(先红后绿)。
- 零新依赖;proxy/server 层零改动(事件字节流经既有透传通道)。
