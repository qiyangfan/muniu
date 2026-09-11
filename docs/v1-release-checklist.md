# v1.0.0 发版判据核对表(T5.6)

> 生产承载观察期结束时逐项核对;全部勾选后由用户执行 tag v1.0.0 与 push。
> 观察期起算日:__________ 结束日:__________(连续 14 天)

## 1. 生产承载(Phase 5 闸门判据 1)

- [ ] kongming 全量模型流量经 muniu,连续 14 天运行(灰度一周 + 全量一周)
- [ ] 每日日志证据留存:`sqlite3 data/muniu.db "SELECT date(created_at),COUNT(*) FROM usage_logs GROUP BY 1 ORDER BY 1;"` 摘要贴入下方
- [ ] 零人工干预(无手动重启/手动改配置/紧急修复);任何干预事件记入下表即视为中断,需重新计时

| 日期 | 请求数 | 错误率 | 换道次数 | 缓存命中 | 干预事件 |
|---|---|---|---|---|---|
|  |  |  |  |  |  |

## 2. 对账(闸门判据 2)

- [ ] 每日执行 `scripts/reconcile.sql`,差异 ≤1%(muniu usage_logs vs kongming 用量记录)
- [ ] 对账记录:__________(差异说明:__________)

## 3. 压测(闸门判据 3)

- [ ] 报告:`docs/benchmarks/2026-09-10.md`(muniu 单边 + one-api 对照已完成)
- [ ] README 关键数字:已回填(吞吐峰值 38,498 rps@1000 并发 / 首字节 p50 1.15ms@100)
- [ ] 对照组 one-api:✅(2026-09-10 官方 arm64 二进制;如后续有 amd64 主机可复核 x86 形态)

## 4. 设计文档系列(闸门判据 4)

- [x] `docs/articles/01-first-token-boundary.md` 首 token 边界
- [x] `docs/articles/02-streaming-usage-metering.md` 流式 usage 计量
- [x] `docs/articles/03-conformance-as-asset.md` conformance 资产化
- [x] `docs/articles/04-single-binary-tradeoffs.md` 单二进制取舍
- [ ] 外发(掘金/博客):用户决定,非发版前置

## 5. 质量门禁(闸门判据 5)

- [ ] 发版当日 `make check` 全绿(20 包含 web typecheck/lint)
- [ ] `make chaos` 全绿
- [ ] conformance 全 fixture 绿(≥6 家)

## 6. 文档一致性(闸门判据 6)

- [x] 2026-09-10 一致性核对完成:`docs/design-consistency-check-2026-09-10.md`
- [ ] v1.0.0 前建议复核一遍(若 Phase 4-5 间无实现变更可引用本次记录)

## 收尾

- [ ] README 顶部「施工中」横幅撤下,改为稳定性声明(引用两周数据摘要)
- [ ] tag v1.0.0 与 push:**由用户执行**
