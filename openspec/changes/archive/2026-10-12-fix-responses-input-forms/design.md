# Design

## Context

kongming e2e 实证的两处误拒均位于 `internal/protocol/responses.go` 条目解析:`switch it.Type` 对空串(省略 type)落 `default` 报 `input_item_unsupported`;`respItem.Content []respPart` 遇字符串 content 时整个 items unmarshal 失败报 `input_invalid`。同轮排查确认 anthropic 入站已正确处理字符串快捷形态(`anthropic_in.go` 有专门分支),缺陷面仅 responses.go 一处;百炼 responses 原生端点的多轮回放 400 属上游服务端缺口,与本仓无关但需知识注记防误判。

## Goals / Non-Goals

**Goals:**

- 官方 SDK 缺省形态(省略 type、字符串 content)全链放行,与显式形态 canonical 逐字节同构。
- 拒绝清单、错误码、信封形态、既有显式形态行为零改动。

**Non-Goals:**

- 不做类型猜测(缺省 type 仅按 role 判 message,不按 call_id 等字段猜 function_call)。
- 不触碰响应反向映射、出站路径、`internal/proxy` 翻译层;不处理百炼服务端缺口(跨仓边界外)。

## Decisions

### 决策 1:修复收敛在解析层一处,形态折叠前置于翻译

`respItem.Content` 改为 `json.RawMessage` 二态解析:字符串→折叠为单 text 部件;数组→现行 parts 路径;数字/null/对象等→现行 `input_invalid`。`type` 缺省判定:空串 ∧ role 非空→`message`;空串 ∧ role 空→`input_invalid`(function_call/function_call_output 官方形态恒带 type,不猜测)。折叠发生在解析期,后续翻译代码对两形态无感——映射表其余部分零改动。备选「翻译层各消费点分别兼容两形态」否决:散点兼容必漏,且 canonical 同构性难钉。

### 决策 2:等价性以对拍测试钉死,而非仅单测放行

同语义请求的快捷形态与显式形态必须产出逐字节相同的 canonical(因此缓存指纹、计量、治理全部天然同构)。单测只断言"能过"不足以防止两形态未来漂移(如字符串形态漏转义),对拍是漂移探测器。kongming 侧绕行(发显式形态)无须回滚:显式形态对 OpenAI 官方/百炼/muniu 三方兼容面最宽,修复后仍正确——结论写入文档注记。

## Risks / Trade-offs

- [二态解析引入过宽接受面] → 拒绝负面用例全量保留并新增(数字/null content、缺 type 缺 role);危险清单(previous_response_id/background/未知 item 类型)回归钉死;对拍测试防形态间漂移。
- [与在途提案冲突] → 无文件交集(responses.go 不在 passthrough/gemini 两变更触达面内),apply 顺序无关;三者并行时本变更最小,建议先行。

## Migration Plan

纯缺陷修复,无配置/存储/契约迁移;回滚即代码回退。验收:新增失败测试先红后绿 + 既有 responses 全量测试零改动回放 + `make check` 全绿;真机复核可复用 kongming e2e 配方(env 三变量起 muniu + 官方 SDK 缺省形态直发)。

## Open Questions

无。
