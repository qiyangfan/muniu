# ADR-0003:适配器三分法——OpenAI 兼容基座 + quirk patch + 协议桥接

- 状态:已接受(2026-09-08);2026-09-30 更正——桥接入驻方仅 Anthropic(下文「仅 Anthropic 与 Gemini 入驻」为初稿设想),Gemini 经官方 OpenAI 兼容端点接入;三分法形态枚举与本决策结论不变。依据:`docs/design/upstream-adapters.md`「Gemini 接入方向」(upstream-egress-batch 裁决)与 openspec change `retire-gemini-adapter-token`。

## 决策

出站适配器只有三种形态:①`openai.go` 单一兼容基座(可配 base_url);②per-provider `quirks/` 差异补丁(usage 字段、流式 reasoning、错误码),只声明与基座的差异;③`bridge/` 全量协议翻译,仅 Anthropic 与 Gemini 入驻。客户端可见字段默认零改写(passthrough),改写仅发生在桥接型。

## 理由

- 国产五家(DeepSeek/Moonshot/GLM/Qwen/Doubao)均提供 OpenAI 兼容端点,真实差异集中在少数字段——one-api 式「每家复制一套完整适配器」造成 N 份同构代码,一次基座修 bug 要 N 次同步,是可预见的维护灾难;
- quirk 层把「差异」变成显式数据(可被 conformance fixture 逐条断言),兼容正确性从「口碑」变成「测试集」——这正是 muniu 差异化(国产协议 conformance)的架构落点;
- 桥接型全翻译的复杂度是真复杂度(事件机差异),集中隔离在 bridge/ 包,不污染基座。

## 备选与否决

- per-provider 全量适配器(one-api 式):同构复制,否决如上;
- 统一内部中间表示(IR)再各自翻译:多一层抽象只服务两家桥接,过度设计;若未来桥接家数 ≥4 重评。

## 后果

基座接口必须稳定(adapter 接口五方法为契约);新「OpenAI 兼容」供应商接入成本 ≈ 一份 quirk 文件 + 一组 fixture,这是对外可讲的扩展性故事。
