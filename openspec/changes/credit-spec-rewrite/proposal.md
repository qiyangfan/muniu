# credit-spec-rewrite 提案:主规格记账单位回写(μ → credit)

## Why

计量基座已由 μ(货币百万分之一)整体切换为 credit(1 credit = 本位币 1e-9,迁移 0007,存量 ×1000),代码、`docs/design/metering.md`、控制台均已收敛;但**主 specs 库未随迁**,规格与实现口径分裂(2026-09-13 聚合复查确认):

- `openspec/specs/pricing-management/spec.md`:「价目 ≤6 位小数」(实现 ≤9 位,`internal/metering/price.go` ParsePrice)、「货币百万分之一 μ」(实现 credit 1e-9)。
- `openspec/specs/pricing-time-windows/spec.md`:两处示例场景仍以 μ 计价(3μ→2μ、cost_micro=43)。
- 同类漂移已由 HANDOFF 验收记录 27 登记过文档层残留(semantic-cache / traffic-governance 等),但**主 specs 本身不在该次登记范围**——规格即事实源的纪律下,这是欠账而非可豁免项。

同因跨仓漂移:`docs/adr/0006-self-verifying-keys.md:11` 的 mk2 载体定义仍写 `daily_quota_micro`,代码(`internal/auth/platform.go:3`)已是 `daily_quota_credits`。本仓 AGENTS 与锦官仓共同纪律「跨仓契约变更须双方 change 同步,禁止单边漂移」——credit 迁移即一次实证单边漂移,本变更一并收口。

## What Changes

1. 主 specs 两个 capability 的记账单位口径回写(≤6 位→≤9 位、μ→credit 1e-9、示例场景数字按 credit 口径改写,数学形态与断言行为不变)。
2. ADR-0006 载体字段文本 `daily_quota_micro` → `daily_quota_credits`(笔误级修订,ADR 顶部加一行修订注说明)。
3. 全仓文档 μ 残留清扫:grep `μ|micro` 于 `docs/`、`README.md`,HANDOFF 27 已登记残留项归并本变更一次清零。

## Non-goals

- 不改任何代码(实现已正确,纯规格/文档回写)。
- 不改已归档 change 的文本(归档是历史快照,不改写历史)。
- 不在本次处理锦官侧同因漂移(锦官仓并行立项 credit-spec-rewrite,内容与本变更互为镜像)。

## Capabilities

### Modified Capabilities

- `pricing-management`:校验口径(≤9 位小数)与记账单位(credit 1e-9)回写。
- `pricing-time-windows`:示例场景数字改写为 credit 口径。

## Impact

- 文件:`openspec/specs/pricing-management/spec.md`、`openspec/specs/pricing-time-windows/spec.md`(经 delta 归档同步)、`docs/adr/0006-self-verifying-keys.md`、`docs/` 下 μ 残留文档。
- 风险:零(纯文本回写,行为契约不变;示例数字保持原数学形态:3→2 ceil 与 86→43 半价锚点逐一对应)。
