## ADDED Requirements

### Requirement: 视觉输入价目字段与回落

价目管理必须 (MUST) 增加可选 `vision_input` token 单价；缺省时必须 (MUST) 回落到 `input`。该字段必须 (MUST) 在 YAML、运行态 DB、管理 GET/PUT、OpenAPI 与控制台中保持同一含义，并沿用现有费率校验、币种、时段窗口、覆盖和 credit 整数运算纪律。

#### Scenario: 视觉单价缺省回落

- **WHEN** 别名只配置 `input` 而未配置 `vision_input`
- **THEN** 视觉 token 使用 `input` 单价，既有别名的实际费用保持不变

### Requirement: 多模态 token 分段整数计价

含 `vision_tokens` 的用量必须 (MUST) 将输入拆为文本、缓存和视觉三段并分别向上取整：`text_tokens=prompt_tokens-cached_tokens-vision_tokens`；`cost=ceil(text×input/1e6)+ceil(cached×cached_input/1e6)+ceil(vision×vision_input/1e6)+ceil(completion×output/1e6)`。当视觉 token 为 0 时必须 (MUST) 与现有计价结果一致。若 `cached_tokens+vision_tokens>prompt_tokens`，必须 (MUST) 按既有未结算观测纪律拒绝精确计价。

#### Scenario: 文本与视觉按不同单价结算

- **WHEN** `prompt_tokens=1000`、`vision_tokens=400`、`cached_tokens=0`，文本价为 1 credit/百万、视觉价为 2 credit/百万
- **THEN** 成本分别按 600 个文本 token 与 400 个视觉 token 向上取整，合计为 2 credits

#### Scenario: 无视觉用量零回归

- **WHEN** 既有请求 `vision_tokens=0` 并带有输入、缓存输入和输出 usage
- **THEN** 成本与原三段公式逐 credit 相同

#### Scenario: 非法 token 子集不作精确结算

- **WHEN** usage 中 `vision_tokens + cached_tokens > prompt_tokens`
- **THEN** 不产生精确费用，记录现有未结算/estimated 观测语义并输出可核对日志
