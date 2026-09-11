## Why

muniu 的 model_routes 允许一份客户端参数跨供应商换道,而模型个性约束普遍存在:部分模型不吃 temperature(o-series/deepseek-reasoner 类)、部分模型思考不可关(reasoner 类)、部分模型输出上限很小。不治理则换道后要么上游 400、要么行为静默漂移——参数兼容不是客户端责任,而是换道正确性的组成部分。业界两极(one-api 纯透传甩锅客户端 / LiteLLM 全量注册表漂移成 bug 工厂)均不可取,裁决为**配置声明制模型能力目录 + 逐 attempt 参数治理**(ADR-0010,2026-09-11 已接受)。

## What Changes

- YAML 静态拓扑新增**模型能力声明段**(与 providers/model_routes/pricing 同源,ADR-0004 分工):零内置隐式知识,未声明的模型或参数 = 零干预、原样透传。
- proxy 在**每次 failover attempt 出站前**(adapter Prepare 之前)按「目标 provider+model」解析约束并执行治理:幂等纯函数;换道后按新模型重新治理。
- 治理动作三型:**drop**(删除不支持参数)、**clamp**(数值越界收敛到声明区间/上限)、**思考锁**(对"关思考"意图按声明拒绝或归一);默认宽松放行且干预必留痕(结构化日志 + 客户端可见响应头),可配 strict 模式改为 400。
- 响应侧「字段改写白名单」不变;协议形态映射(max_tokens 改名/必填、reasoning_effort→各家方言)仍归 quirk/bridge,不入目录。
- Adapter 接口五方法契约零改动;无新增第三方依赖。

## Capabilities

### New Capabilities

- `model-capability-governance`: 模型能力目录的配置声明语义、逐 attempt 治理点与幂等性、drop/clamp/思考锁动作语义、留痕与 strict 模式、未声明零干预的透传保证。

### Modified Capabilities

(无——openspec/specs/ 当前为空库,本变更为新增能力,不动既有基线。)

## Impact

- 代码:`internal/config`(能力段解析与校验)、`internal/proxy`(attempt 循环内治理钩子)、`muniu.example.yaml`(注释样例)。
- 测试(测试先行域·协议适配):负面用例——声明参数被 drop、数值被 clamp、思考锁生效、换道后按新模型重治理、未声明零干预(透传字节不变);治理纯函数表驱动单测 + attempt 循环集成钉子。
- 契约:upstream-adapters.md「请求参数治理」节(已随 ADR-0010 写入);openapi 无新端点(响应头标记为新增可观测面)。
