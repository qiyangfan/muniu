# 任务清单:credit-spec-rewrite

> 纪律:纯规格/文档回写,零代码改动;每步后 grep 核对无残留;归档后主 specs 与实现口径一致。

## 1. 主 specs 回写

- [ ] 1.1 `pricing-management`:「在线改价即时生效·校验失败整单拒绝」≤6 位→≤9 位;「价目校验纪律」μ→credit(1e-9)、≤6 位→≤9 位;delta 归档同步。
- [ ] 1.2 `pricing-time-windows`:「保守取整」「端到端倍率落账」示例数字改 credit 口径(3μ→3 credit、cost_micro=43→cost_credits=43,数学形态不变);delta 归档同步。
- [ ] 1.3 归档后 grep 两主 specs 无 `μ|micro|6 位` 残留;`cost_micro` 字段名(如计量行 schema 描述中出现)核对为 `cost_credits`。

## 2. ADR 与文档残留

- [ ] 2.1 ADR-0006 载体定义 `daily_quota_micro` → `daily_quota_credits`,顶部加修订注(「2026-09-13 随 credit 基座迁移回写,代码 internal/auth/platform.go 为准」)。
- [ ] 2.2 `grep -rn "μ\|百万分之一" docs/ README.md` 清单化逐条处理;HANDOFF 验收记录 27 已登记的 semantic-cache / traffic-governance 等文档残留归并本任务一次清零;处理不了的(如历史验收记录原文)加修订注不改原文。
- [ ] 2.3 HANDOFF 补验收记录:回写范围、grep 清零证据、与锦官仓 credit-spec-rewrite 的互引(跨仓纪律执行留痕)。
