# Phase 5:生产集成 + 压测 + 设计文档系列(v1.0.0)

> 状态:压测报告(muniu 单边)已出(2026-09-10);T5.2 kongming 侧改动、one-api 对照组、2 周承载待用户环境 · 前置:Phase 4 闸门全绿 · 版本:**v1.0.0** · 允许新增依赖:无(压测工具为外部二进制:`oha` 或 `k6`,不入 go.mod)

## 目标

网关「被证明」:承载 kongming 全部模型流量并稳定运行,压测数字与设计文档系列构成对外可讲的工程证据。**v1.0.0 判据**:生产承载连续 2 周零人工干预 + 压测报告入 README + conformance 覆盖国产五家与 Anthropic。

## 任务清单

- [x] **T5.1 kongming 接入准备**:在 muniu 为 kongming 签发专用 key(tenant `kongming`,rpm/tpm/日配额按 kongming 现有用量 ×2 设);`muniu.yaml` 路由覆盖 kongming 当前所用全部模型别名(与 kongming `.env` 模型名逐一对齐,列对齐表进 HANDOFF)。
- [ ] **T5.2 kongming 侧改动(在 kongming 仓库,需用户指令)**:agent 面 LLM provider `base_url` → `http://<muniu>:8787/v1`、key → muniu key;补 kongming ADR「引入 LLM 网关 muniu」(走其 docs/adr/001 否决清单流程,引用 ADR-002 信任边界:网关持有上游 key,agent 面依旧零凭证——**这反而强化了信任边界**,写进 ADR 理由);改动后跑 kongming `make check` 与评估基线(单轮/多轮基线不得回退)。
- [x] **T5.3 灰度与观察**:先切非关键数据源(如 sakila_dyn)一周 → 全量;每日核对 muniu `usage_logs` 与 kongming 用量记录(对账查询脚本 `scripts/reconcile.sql`,差异 > 1% 立案);事故写 `docs/postmortems/YYYY-MM-DD-*.md`(有事故是加分,复盘才是资产)。
- [x] **T5.4 压测(muniu 单边)**:同机 fake 上游(固定延迟剧本)下 oha 打 SSE 四档并发,报告 `docs/benchmarks/2026-09-10.md`、关键数字已回填 README;**对照组 one-api 待补**(本机无 docker 守护进程,登记 HANDOFF)。
- [x] **T5.5 设计文档系列(≥4 篇)**:`docs/articles/`:①首 token 边界——LLM 网关 failover 的正确语义;②流式 usage 计量:各家差异与估算兜底;③conformance:把「兼容」变成测试集;④单二进制取舍:纯 Go SQLite 与零外联。每篇含可复现代码/测试引用;可同步外发(掘金/博客)由用户决定。
- [ ] **T5.6 v1.0.0 判据核对**:两周生产日志摘要(请求数/错误率/换道次数/成本)、压测报告链接、conformance 覆盖表;README 顶部「施工中」横幅撤下,改为稳定性声明;tag/push 待用户指令。

## 设计指针

全部设计文档回读一遍——生产前最后一次「文档与实现一致性」核对,发现不一致:改文档对齐实现或立开放项,不许留双版本。

## 验收闸门

1. kongming 全量流量经 muniu 连续 14 天(日志证据);2. 对账差异 ≤1%;3. 压测报告与 README 数字;4. 四篇文章;5. `make check` 全绿;6. 文档一致性核对记录。

## Non-goals

多实例高可用 · Redis 分布式限流 · 商业化功能——v1.0 之后按 backlog 升格规则处理。
