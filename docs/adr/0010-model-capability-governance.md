# ADR-0010:模型能力目录与请求参数治理——声明制、逐 attempt、留痕放行

- 状态:已接受(2026-09-11)

## 决策

在「入站协议 → adapter 出站」链路上新增**模型能力层**,与协议方言层(ADR-0003 三分法)正交,分界口诀:**同一供应商的不同模型行为不同 → 能力层;同一参数跨协议表达不同 → 方言层**。

1. **协议方言层(provider 级,既有,不变)**:quirk/bridge 的 Prepare 负责同一参数跨协议的形态映射——`max_tokens`→`maxOutputTokens` 改名、anthropic `max_tokens` 必填、`reasoning_effort`→`enable_thinking`/`thinking.budget_tokens` 方言翻译。
2. **模型能力层(model 级,新增)**:模型目录声明单模型个性约束(参数黑名单、思考锁、输出上限等),由 proxy 在**每次 failover attempt 出站前**按「目标 provider+model」执行治理。治理是幂等纯函数(入:请求+能力约束;出:改写后请求+变更记录);换道到新家后按新模型重新治理。

元数据来源:**配置声明制,零内置隐式知识**——约束全部来自 YAML 静态拓扑的显式声明(与 providers/model_routes/pricing 同源,ADR-0004 分工);未声明的模型或参数 = 零干预、原样透传。干预必留痕(结构化日志 + 客户端可见响应头);可配 strict 模式改为 400 拒绝,默认宽松放行。

## 理由

- **换道正确性是 muniu 的存在理由,参数兼容是其组成部分**:one-api 客户端把渠道选死,参数不兼容是客户端责任;muniu model_routes 一份参数跨家换道,不治理则换道后要么 400 要么行为漂移——所以治理点必须逐 attempt、按目标模型执行,而非入口一次。
- **业界证据**:LiteLLM 全量能力注册表(数千行 json)+ per-provider supported 参数声明,已漂移成 bug 工厂(`drop_params` 对 dimensions/vertex 等多类参数不生效的 issue 成串);one-api 纯透传把兼容问题原样甩给客户端。两者之间取「声明制 + 极小隐面」,契合本项目「少而精、可溯源」的立身之本。
- **分层正交防 LiteLLM 式混层**:把「协议形态映射」和「模型个性」混在一层维护,正是 LiteLLM 维护重灾区的根源;正交后 quirk/bridge 代码不被模型矩阵污染,目录只是一份数据。

## 备选与否决

- LiteLLM 式全量注册表内置(含「内置常识默认」如 reasoner 思考锁):看似贴心,实则又一个漂移面,且违背 ADR-0004「静态拓扑显式声明」的一致性——否决,目录零内置。
- 运行时探测能力 / 上游 400 错误自动学习:复杂不可靠,记 backlog。
- 不支持参数默认报 400(LiteLLM 默认行为):把 failover 打回客户端时代,违背换道初衷——否决;默认留痕放行,strict 可选。

## 后果

- upstream-adapters.md 增「请求参数治理」节:响应侧「字段改写白名单」原文不变,治理是**请求侧**的独立扩展;协议形态映射不入目录,仍归 quirk/bridge。
- 新增配置段(model 能力声明),config 校验加载;strict 默认宽松。
- 测试先行域(协议适配):负面用例必须覆盖——声明参数被 drop、数值被 clamp、思考锁生效、换道后按新模型重治理、未声明零干预(透传字节不变)。
- spec 基线走 openspec change `model-capability-governance`;Gemini bridge 收口、Responses 入站等接入演进与本决策正交,不混入。
