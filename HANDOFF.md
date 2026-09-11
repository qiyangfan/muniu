# HANDOFF — 开工指引(唯一入口)

> 维护纪律:只写「当前状态 / 下一步 / 开放决策项 / 验收记录」四块,不写变更明细(归 git log),单次更新 ≤30 行。

## 当前状态(2026-09-11,推送前全量代码审查)

- **Phase 0-4 代码与验证全部完成**;Phase 5 前置部分完成(见 2026-09-10 记录)。
- **2026-09-11 全仓代码审查循环**:四个并行评审覆盖协议/转发/计量存储/管控/web CI,溯源核验后确认 27 项真实缺陷并全部修复(详见验收记录 11),`make check` 全绿。
- **git 已完成首次全量提交**(2026-09-11,root commit,203 文件,方案 A′);tag v0.3.0 / 建远端 / push 待用户逐次指令。
- Phase 5 剩余:T5.1 末段(kongming `.env` 对齐回填——本机无 kongming 仓库)、T5.2(kongming 侧改动,需用户指令)、T5.3 灰度 1 周+全量(真实环境计时)、T5.6 两周判据核对。
- **对照组 one-api 已补齐**:官方 v0.6.10 darwin-arm64 二进制同机同剧本背靠背对比,muniu 吞吐 80-150×、首字节 p50 低 1-2 个数量级(报告 docs/benchmarks/2026-09-10.md)。
- **压测报告已出**:docs/benchmarks/2026-09-10.md(muniu 单边,oha 四档:峰值 37,951 rps@1000 并发 100% 成功率,首字节 p50 1.13ms@100),关键数字已回填 README。

## 下一步(严格按序)

1. 【用户】GitHub 建仓 + tag + push(**提交已完成**:2026-09-11 root commit;⚠ 实施提示:按方案 A′ 单提交单 tag,v0.1.0/v0.2.0 放弃,发布历史从 v0.3.0 起算):
   ```bash
   git remote add origin git@github.com:solvela/muniu.git
   git tag v0.3.0 && git push -u origin main --tags
   ```
2. 【执行会话】push 后:CI 绿链接回填;README 补 CI badge。
3. 【用户+执行】kongming 侧:`bash scripts/bootstrap-kongming.sh`(签发 key)→ kongming 改 base_url/key + ADR(T5.2,需用户指令)→ 灰度一周→全量(T5.3,每日对账)。**ADR 草案已备**:`docs/kongming-adr-draft.md`(引入 muniu 的完整论证含否决清单与信任边界分析,拷入 kongming docs/adr/ 即可)。
4. 【执行会话·已完成】压测:muniu 单边 + one-api 对照组均已出(见验收记录)。
5. 【跨会话】两周生产观察期计时 → T5.6 v1.0.0 判据核对 → tag v1.0.0 待用户指令。

## 开放决策项

- DeepSeek 峰谷四档计价 pricing 扩列(保守上限已实现);Anthropic 文档区域屏蔽待复核;版本拆分方案 A/B 终裁(建议 A)。

| 日期 | 现象 | 影响 | 建议 | 裁决 |
|---|---|---|---|---|
| 2026-09-10 | DeepSeek 峰谷四档计价 | pricing 单列表达不了 | 扩 peak/offpeak 列 | 待裁决 |
| 2026-09-10 | Anthropic 文档区域屏蔽 | 桥接无法逐字复核 | 可访问后复核 | 待裁决 |
| 2026-09-10 | 零提交下跨阶段开发 | tag 语义纯净性 | 方案 A 拆分提交 | 待裁决 |
| 2026-09-10 | one-api 无 arm64 镜像 | ~~对照压测需 amd64 主机~~ **已解决**:改用官方 darwin-arm64 二进制 | 已完成 | 已解决 |
| 2026-09-11 | usage_logs 实际每上游尝试一行,metering.md 写「每请求一行」 | 对账/报表按行计数口径失真(failover 重试放大) | 改文档为「每尝试一行」,对账按 DISTINCT(request_id)(reconcile.sql 已按此修正) | 待裁决 |
| 2026-09-11 | 桥接工具对话往返未翻译(role=tool / assistant.tool_calls 请求侧) | 多轮工具对话经 anthropic 桥接不支持(响应侧 tool_use 翻译已完整) | 已登记 bridge/README 限制;完整翻译需设计决策(Anthropic 交替结构语义) | 待裁决 |
| 2026-09-11 | 语义缓存命中行无延迟字段(TotalMs 恒 0) | 命中延迟不可观测 | usage_logs 加列需新迁移,评估是否值得 | 待裁决 |
| 2026-09-11 | DeepSeek 402(余额不足)归 ClassUnknown,key 不冷却 | 欠费 key 被持续重试 | quirk 层覆写 Classify:402→auth_failed | 待裁决 |
| 2026-09-11 | reconcile.sql 口径修正(每尝试一行→DISTINCT) | 旧口径 ≤1% 判据不可达 | 已按「每尝试一行」实现修正 SQL | 已解决 |

## 验收记录(阶段闸门时追加:判据 → 证据 → 日期)

**Phase 0(2026-09-09)**:go1.27.1/golangci-lint 2.13.2/healthz 原文/tokenctl 实录/make check 绿。
**Phase 1(2026-09-09,全过)**:13 用例 -race;半帧不解析半事件;ollama 真上游实录;未知字段零丢失;基准 6.2ms/千事件。
**Phase 2(2026-09-10,全过)**:6 fixture;牌价核对表;usage_logs 两行实测;鉴权负面+限流边界;三基准;矩阵回填。
**Phase 3(2026-09-10,全过)**:判定表 26 格;chaos 绿+真上游换道实录;11 fixture;TPM/配额边界;矩阵 ⚠ 清零。
**Phase 4(2026-09-10,全过)**:漂移门禁(先红后绿);HNSW 选型(p99 0.41ms vs 暴力 21ms);语义缓存真实命中实录(4 行计量,命中行 cost 0);控制台四页截图(docs/screenshots/);make check 含 web 全绿;管理 API 9 项 curl 实录。
**Phase 5 前置(2026-09-10,压测执行更新)**
1. T5.1:scripts/bootstrap-kongming.sh(建租户+签 key,限额按用量 ×2 待回填);docs/kongming-alignment.md 对齐表模板(本机无 kongming 仓库,⏳ 待回填)。
2. T5.3:scripts/reconcile.sql(按日对账+错误分类 Top;判据差异 ≤1%);docs/postmortems/TEMPLATE.md。
3. T5.4:scripts/bench/fake_upstream.py(固定延迟 SSE 假上游,零依赖)+ run_oha.sh(四档并发)+ README.md 方法/指标/局限;oha 未装(brew install oha 即可)。
4. T5.5:docs/articles/ 四篇——①首 token 边界 ②流式 usage 计量 ③conformance 资产化 ④单二进制取舍,均含可复现测试/代码引用。
5. 文档一致性核对(设计指针要求,记录见 `docs/design-consistency-check-2026-09-10.md`):architecture.md 包结构补 server/admin/web;metering.md 计价公式对齐三段 cached;data-and-config.md 补 semantic 节——三处均「改文档对齐实现」,已记录。
6. `make check` 全绿(20 包含 web typecheck/lint,2026-09-10 终跑);`make chaos` PASS。
7. **压测执行实录(2026-09-10,oha 1.16.0)**:四档并发(100/500/1000/5000,每档 20×并发数)对 SSE 流式转发——成功率 100/100/100/99.4%,首字节 p50 1.13/6.02/15.21/70.41ms,p99 442/141/172/638ms,吞吐 1,481/37,226/37,951/19,897 rps;峰值 3.8 万 rps,报告 `docs/benchmarks/2026-09-10.md`,原始 JSON 在 `raw-20260910-033943/`。one-api 对照组待 docker 环境(登记待补)。
8. **对照组压测(2026-09-10 补齐)**:one-api v0.6.10 官方 darwin-arm64 二进制同机同剧本——muniu 峰值 38,498 rps(100% 成功)vs one-api ~456-482 rps;首字节 p50:muniu 1.15-82ms vs one-api 8.98ms-9.76s;5000 档 one-api 降至 99.8%。完整对照表见 docs/benchmarks/2026-09-10.md。
9. **v1.0.0 判据核对表**:`docs/v1-release-checklist.md`(两周观察期结束后的机械核对清单);**一致性核对记录**:`docs/design-consistency-check-2026-09-10.md`(Phase 5 闸门判据 6 的正式记录文件)。
10. **推送前预检(2026-09-10)**:待入库 202 文件扫描——无真实密钥(auth_test.go 命中为「不存在」用例夹具假 key)、无大二进制/DB/.env;.gitignore 覆盖完备(muniu.yaml/data/node_modules/dist 均不入库);`make check` 重跑全绿(golangci-lint 0 issues + web typecheck/lint 通过 + 20 包 race test 全 ok);git 状态 0 提交/0 tag/0 远端,铁律 4 合规。可安全执行『下一步』1。
11. **全量代码审查循环(2026-09-11,三轮收敛)**:第 1 轮四域并行评审确认 27 项缺陷,第 2 轮复审修复增量补 4+2 项,第 3 轮聚焦复审补 1 项,全部修复。代表性十项——①AccessLog 的 statusWriter 不透传 Flusher,生产链路 SSE 逐事件冲刷全失效(攒包转发);②桥接流式 usage 从原始事件抽取(Anthropic usage 嵌套/键名不同)致桥接流计量静默归零;③桥接流式 tool_call 翻译缺 id/name 且并行调用折叠 index 0(工具流不可用);④流式响应从不入语义缓存(注释声称的能力不存在);⑤语义缓存流式回放按 64 字节硬切劈开 UTF-8,中文回放乱码;⑥租户 disabled 对数据面无效 + 吊销 key 后缓存 10s 内继续放行(Invalidate 零调用);⑦UsagePoint 缺 json tag,管理 API 输出 PascalCase,控制台用量页全空;⑧attempt 早退路径「写信封+返回错误类」致双信封/502 头+成功体拼接;⑨wireOf 判定顺序使空载荷心跳漏透原字节;⑩admin 错误出口未分类,内部故障以 400+驱动错误文本外泄(usage from/to 未归一 UTC 同批修)。其余:embeddings 无首 token 超时、流式非 200 重发请求、断连记 key EMA 失败、tools 请求可被语义缓存错配、metering 刷盘配置未接线、ParsePrice 溢出负价、semantic_cache 过期行永不清理、`%q` 构嵌入请求体、admin 500 泄内部错误、负额度=不限、web CI 门禁缺失、release 不构建控制台等(全量见 `make check` 绿后代码)。新增回归钉子测试 12 个;fixture tool-stream 更新钉住 content_block_start 翻译。终验:`make check` 全绿(vet+lint 0 issues+web+20 包 race)。
