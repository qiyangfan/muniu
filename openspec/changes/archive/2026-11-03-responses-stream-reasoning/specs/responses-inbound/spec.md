# Spec Delta

## ADDED Requirements

### Requirement: 流式 reasoning_content 透传事件序列

`/v1/responses` 入站流式翻译必须 (MUST) 把上游 `delta.reasoning_content` 增量映射为官方事件词汇序列:`response.output_item.added`(item `type=reasoning`,`item_id=item_reasoning`)→ 逐增量 `response.reasoning_summary_text.delta` → 收束 `response.reasoning_summary_text.done`(text=累计原文)+ `response.output_item.done`(item 形态与非流式冻结契约同形:`type=reasoning`+`text`)。`response.reasoning_summary_part.*` 事件族不在契约内(登记不实现,客户端宽松接受)。

#### Scenario: R1 推理先于正文的标准序列

- **WHEN** 上游流依次为 reasoning_content 增量×2 → content 增量 → finish
- **THEN** 事件序列为 added(reasoning) → delta×2 → summary done → output_item.done(reasoning,含累计原文) → added(message) → output_text.delta → output_item.done(message) → completed

### Requirement: 推理与正文混流的对称收束

推理与正文/工具调用混流时,翻译层必须 (MUST) 先完整收束当前 item(output_item.done)再开启下一 item,事件流内 item 生命周期不得交叉。

#### Scenario: 正文中途切换回推理

- **WHEN** 流为 content 增量 → reasoning_content 增量 → finish
- **THEN** message item 先完整收束(done)再开 reasoning item(混流不交叉污染)

### Requirement: usage 口径不受 reasoning 透传影响

`response.completed` 的 usage 口径必须 (MUST) 与透传前一致:reasoning_tokens 仅入 output_tokens_details 明细,不得重复计入 output_tokens。

#### Scenario: usage 明细单列

- **WHEN** usage 携带 reasoning_tokens=3、completion_tokens=2
- **THEN** completed.usage.output_tokens=2,reasoning_tokens 仅入 output_tokens_details
