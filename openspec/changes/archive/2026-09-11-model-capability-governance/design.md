## Context

适配链路现状:入站 OpenAI 形态 → proxy 组装 → (failover attempt 循环:adapter.Prepare 出站改写 → 上游调用 → 失败按判定表换道)。ADR-0003 三分法管协议方言(quirk/bridge),ADR-0004 定「静态拓扑走 YAML」;本变更为其上的模型能力层,立法见 ADR-0010,行为契约见本变更 specs。动机不再复述(proposal.md - Why)。

## Goals / Non-Goals

**Goals:**

- 能力目录的配置形态与校验纪律(YAML 静态拓扑一段,零内置,加载期拒绝非法条目)。
- 治理钩子在 attempt 循环中的精确落点与幂等语义(换道重治理)。
- drop / clamp / 思考锁三类动作语义,宽松默认 + strict 全局开关。
- 留痕:结构化日志字段 + 客户端可见响应头。
- 零新增第三方依赖。

**Non-Goals:**

- 自动探测能力 / 上游 400 错误学习(backlog,ADR-0010 已否决入本期)。
- 能力目录运行时热更(定性为静态拓扑,随进程生命周期;动态化等有真实运营诉求再议)。
- Gemini bridge 收口、Responses API 入站等接入演进(与本能力正交,不混入本变更)。
- per-provider/per-tenant 的 strict 粒度(先全局,出现真实诉求再分级)。
- 语义缓存 key 计算变更(见 Risks)。

## Decisions

1. **治理落点:attempt 循环内、adapter.Prepare 之前**,签名形如 `govern(req *protocol.ChatRequest, c Capability) (changes []ChangeRecord)`——在 OpenAI 统一形态上操作,参数名天然使用入站语义;备选「放 adapter 内部」否决:每家重复或基座被迫知道模型个性,错层;备选「入口一次」否决:换道后失效。
2. **幂等靠原地收敛天然达成**:drop 已删字段=无操作,clamp 已收敛值=同值;attempt 重试从入站原请求出发的现有行为不变,治理重复执行无叠加效应,无副本开销。
3. **能力条目键 = (provider, model) 二元组,必填,不做通配**:显式、可审计;模型名跨 provider 冲突时零歧义。少一行声明换掉一整套通配语义,值。
4. **YAML 形态(示意,字段微调为实现级)**:
   ```yaml
   models:
     - { provider: deepseek, model: deepseek-reasoner,
         drop_params: [temperature, top_p], thinking_lock: true }
     - { provider: dashscope, model: qwen-vl-max, max_output_tokens: 8192 }
   strict_governance: false   # 全局开关,默认宽松
   ```
5. **协议规则不入目录**:anthropic thinking 模型 temperature 必须为 1 之类「协议对全体思考模型的规则」归 bridge(它本来就在做形态映射);目录只收「单模型个性」。分界口诀进宪法(upstream-adapters.md),代码评审据此归层。
6. **留痕双通道**:日志走既有 access log 旁路结构化字段(request_id/provider/model/param/action/原值);响应头 `X-Muniu-Normalized`(逗号列参数名)在响应写头阶段注入,流式首字节前可写,无兼容问题。
7. **strict 语义 = 干预点前移为拒绝**:同一套约束解析,strict 时不再改写而是按统一错误信封返回 400;实现上是一处分支,不是两套逻辑。
8. **测试组织**:治理纯函数表驱动单测(specs 五类负面场景逐一钉死)+ config 校验负面单测 + attempt 循环集成钉子(换道重治理、响应头、strict);不改 conformance 资产(那是上游协议差异的域,治理不是上游差异)。

## Risks / Trade-offs

- [静默 drop 改变客户端语义] → 双通道留痕 + strict 可选;文档明示「宽松是默认取舍」。
- [声明漂移(上游模型升级后约束失真)] → 声明制下漂移面收敛为运营者自己的 YAML;留痕让「哪些参数正被干预」随时可查,漂移可被发现。
- [clamp 影响计费预期] → 计量按实际 usage 落账,clamp 只影响请求不触碰计量口径。
- [语义缓存交互] → 缓存 key 仍基于入站原请求与模型别名,治理不参与 key;命中回放的是 OpenAI 形态响应,无需治理,零改动。
- [(provider, model) 二元组不通配] → 同一模型多 provider 需重复声明;换取零歧义与可审计,量小不痛。

## Open Questions

(无——strict 粒度、热更、探测学习均已裁决为非目标,无待定项。)
