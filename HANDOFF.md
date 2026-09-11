# HANDOFF — 开工指引(唯一入口)

> 维护纪律:只写「当前状态 / 下一步 / 开放决策项 / 验收记录」四块,不写变更明细(归 git log),单次更新 ≤30 行。

## 当前状态(2026-09-11,平台化定位裁决 + 动态改价)

- **ADR-0005 已采纳**:muniu 定位 = LLM 网关**数据面**,双形态(standalone 单二进制默认 / cluster 无状态副本);平台能力(用户/钱包/支付/门户)归独立控制面产品(新仓库,PG 权威)。宪法非目标已相应修订。P1 演进项:自校验 key、配置源抽象、计量汇插拔;前置 ADR 0006/0007/0008 待立。
- **动态改价已实现**(验收记录 14):价目 = 运营态,`PUT/DELETE /admin/v1/pricing/{alias}` + 控制台「价目管理」页,即时生效零重启;YAML 降级静态兜底。
- **分时段倍率已实现**(验收记录 13):`timezone` + `time_windows[{window, multiplier}]`,加载期换算、热路径零改动。
- **Phase 0-4 代码与验证全部完成**;Phase 5 前置部分完成(见 2026-09-10 记录)。
- **2026-09-11 全仓代码审查循环**:四个并行评审覆盖协议/转发/计量存储/管控/web CI,溯源核验后确认 33 项真实缺陷并全部修复(详见验收记录 11),`make check` 全绿。
- **git 已完成首次全量提交**(2026-09-11,root commit,203 文件,方案 A′);tag v0.3.0 / 建远端 / push 待用户逐次指令。此后 09-11 的裁决与功能改动**未提交**(29+ 文件)。
- Phase 5 剩余:T5.1 末段已完成(2026-09-11 kongming 会话:alignment 表已回填,muniu.yaml 已补 dashscope 上游[openai 基座直连 compatible-mode]+三别名路由 qwen-plus/qwen-vl-max/text-embedding-v2,牌价走运营态待控制台录入;两文件未提交);T5.2 进行中(kongming 侧 ADR-007 已落盘,.env 三处切换待 muniu 启动+bootstrap 签 key 后执行);T5.3 灰度 1 周+全量(真实环境计时)、T5.6 两周判据核对。
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

开放决策项已于 2026-09-11 全部终裁(裁决理由见下表与验收记录 12),当前无待裁决项。

| 日期 | 现象 | 影响 | 建议 | 裁决 |
|---|---|---|---|---|
| 2026-09-10 | DeepSeek 峰谷四档计价 | pricing 单列表达不了 | 扩 peak/offpeak 列 | **用户拍板开放,已实现通用分时段倍率**(2026-09-11 二次裁决,推翻本席「维持单列」):选倍率而非逐窗全价、固定偏移时区、先声明先匹配、加载期换算+保守 ceil、计价热路径零改动;无 `time_windows` 即恒缺省价,单列保守计价仍是合法形态。设计契约见 metering.md「分时段倍率」 |
| 2026-09-10 | Anthropic 文档区域屏蔽 | 桥接无法逐字复核 | 可访问后复核 | **关闭,改以真上游为准**(2026-09-11 复测区域屏蔽仍在):文档不是权威源,真实上游才是。触发条件改为「首次接入真实 anthropic 上游前跑 `make smoke PROVIDER=anthropic` + 首周对账」,不再等文档 |
| 2026-09-10 | 零提交下跨阶段开发 | tag 语义纯净性 | 方案 A 拆分提交 | **采纳 A′**:已按单提交归档(2026-09-11),发布历史从 v0.3.0 起算;跨阶段文件演化下三 tag 拆分语义失真,否决 |
| 2026-09-11 | usage_logs 实际每上游尝试一行,metering.md 写「每请求一行」 | 对账/报表按行计数口径失真(failover 重试放大) | 改文档为「每尝试一行」 | **改文档对齐实现**(已完成):每行如实对应一次真实上游调用的成本归因,合并行会丢重试链路可追责性——事件级明细+读侧聚合是大厂用量计费标准做法;读侧口径「请求=DISTINCT(request_id),token/成本取终态成功行」已写入 metering.md 与 reconcile.sql |
| 2026-09-11 | 桥接工具对话往返未翻译(role=tool / assistant.tool_calls 请求侧) | 多轮工具对话经 anthropic 桥接不支持(响应侧 tool_use 翻译已完整) | 已登记 bridge/README 限制 | **维持登记限制,进 backlog**(无现状消费者;kongming 无桥接+工具多轮场景)。半正确实现比诚实限制更危险(Anthropic 交替结构/id 配对易错)。升级条件:出现真实需求即立项,设计+fixture 先行 |
| 2026-09-11 | 语义缓存命中行 TotalMs 恒 0 | 命中延迟不可观测 | usage_logs 加列评估 | **无需迁移,直接测量**(已实现):`latency_total_ms` 列本就存在,命中行硬编码 0 是实现遗漏。recordCacheHit 现记「请求入口→命中判定」真实耗时(含嵌入自调用,命中路径主要延迟),兑现 semantic-cache.md「命中延迟单独字段」契约 |
| 2026-09-11 | DeepSeek 402(余额不足)归 ClassUnknown,key 不冷却 | 欠费 key 被持续重试 | quirk 层覆写 Classify:402→auth_failed | **402→ClassRateLimited**(非 auth_failed,已实现):欠费是「充值即恢复」的暂态——auth_failed 会永久禁用 key(需人工重启恢复),与真实生命周期相反;限流冷却指数退避自动重探,即熔断器半开语义。判定表零改动,quirk 层落地+测试钉死 |
| 2026-09-11 | reconcile.sql 口径修正(每尝试一行→DISTINCT) | 旧口径 ≤1% 判据不可达 | 已按「每尝试一行」实现修正 SQL | 已解决(口径随上项一并写入 metering.md) |
| 2026-09-11 | 模型个性约束(参数黑名单/思考锁/输出上限)无治理层,跨家换道后 400 或行为漂移 | 换道正确性缺口——参数兼容是 model_routes 换道正确性的组成部分 | 声明制能力目录 + 逐 attempt 治理(业界对标:one-api 纯透传甩锅客户端 / LiteLLM 全量注册表漂移,均否决) | **ADR-0010 已立**(零内置声明制、换道重治理、留痕放行+strict 可选);宪法 upstream-adapters.md 增「请求参数治理」节;openspec change `model-capability-governance` 已立(proposal/specs/design/tasks 齐全,validate 通过),**已实施并通过门禁(2026-09-11,见验收记录 18)** |

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
12. **开放决策项全部终裁(2026-09-11,架构师视角,7 项)**:峰谷计价~~维持单列保守~~(**当次被用户推翻**,见 13);Anthropic 复核**关闭改以真上游为准**(区域屏蔽 2026-09-11 复测仍在,smoke+首周对账兜底);tag **采纳 A′**(已归档);usage_logs **改文档对齐「每尝试一行」**(事件级明细+读侧聚合=大厂用量计费标准做法,口径入 metering.md/reconcile.sql);桥接工具往返**维持登记限制进 backlog**(无现状消费者,半正确实现比诚实限制危险);命中延迟**无需迁移直接测量**(`latency_total_ms` 列已存在,recordCacheHit 记真实耗时);DeepSeek 402 **quirk 归限流冷却**(充值即恢复的暂态;auth_failed 禁用是人工介入级,与 key 真实生命周期相反;熔断半开语义,判定表零改动)。落地:quirks.go(classify 挂点+402 映射+TestQuirk_DeepSeek402)、proxy/cache.go+proxy.go(命中延迟)、metering.md(行口径节)。终验:`make check` 全绿。
13. **分时段倍率计价(2026-09-11,用户拍板开放)**:pricing 增 `timezone` + `time_windows[{window, multiplier}]`。设计取舍——倍率(基点)而非逐窗全价(厂商错峰=整卡折扣)、固定偏移时区不引 tzdata、多窗先声明先匹配(免跨午夜区间代数)、加载期一次性换算各窗有效三段价(保守 ceil,窗口价永不低估)、`CostWithCached` 热路径零改动(仅 record 时查表)、cached 缺省=input 语义窗内保持。落地:metering/pricing_window.go(ParseMultiplier/ParseWindowMinutes/ParseZoneOffset/ScalePrice/PricingTable)+契约测试 8 组;config 校验聚合(引 metering 解析器,无循环依赖);proxy.Config.Pricing 改 PricingTable;装配层构造;端到端钉子 TestRegress_分时段倍率落账(全天半价窗 86μ→43μ)。文档:metering.md「分时段倍率」、muniu.example.yaml 注释样例。终验:`make check` 全绿。
14. **动态改价(2026-09-11,用户需求「厂商改价不碰 Nacos/不重启」)**:价目定性为**运营态数据**(ADR-0004 分工边界),SQLite `model_pricing` 表(迁移 0004)为运营覆盖,YAML pricing 降级静态兜底(按别名回落)。链路:`PUT/DELETE /admin/v1/pricing/{alias}`(先校验后持久化再刷新)+ `PricingDirectory` 原子替换(在途请求旧表完成,下一请求新价,零重启);控制台新增「价目管理」页(列表/编辑/撤销覆盖);openapi.yaml 同步 + api-types 重新生成(路由漂移门禁通过)。兜底纪律:重建遇非法行整体失败保持旧目录(脏数据不进热路径)。落地:metering/pricing_directory.go、store/pricing.go、admin/pricing.go、cmd/muniu/pricing.go(合并语义)、测试 4 组(store CRUD/admin 全流程/装配合并/目录与格式化)。文档:metering.md「动态价目」。终验:`make check` 全绿。
15. **平台化定位裁决(2026-09-11,ADR-0005)**:muniu = LLM 网关**数据面**,双形态——standalone(单二进制+SQLite,默认一等公民)/ cluster(无状态副本 + 控制面下发,SQLite 降级本地缓冲兜底);平台能力(用户/钱包/支付/门户)归独立控制面产品(新仓库,PG 权威),muniu 宪法不商业化。证据基线:Envoy+xDS、Kong DB-less、Nacos(Derby 单机/MySQL 集群)、Grafana(内嵌默认+共享库 HA)、Gitea(反例)、one-api/LiteLLM/Portkey/Helicone(同赛道分离式收敛)。宪法非目标已修订(集群数据面移出、平台功能指向 ADR-0005)。后续 ADR:0006 自校验 key/0007 计量汇插拔/0008 集群限流。**范围修订(同日,用户裁决):支付不真实落地,预留 `PaymentProvider` 抽象接线**——账本只认入账事件,MVP 以管理员手工入账兜底,真实 PSP = 未来实现一个 adapter。本条为立法,无代码改动。
16. **P1 状态分层实现(2026-09-11,openspec platform-data-plane 批次一)**:①自校验平台 key(mk2- HMAC 载体,离线验真+吊销版本快照,与 mk- 查库 key 并存,standalone 零回归);②计量汇插拔(Sink 抽象:本地 SQLite/控制面 ingest;失败整批落 `metering_buffer` 缓冲表(迁移 0005),恢复按序续传、远端幂等去重;容量满背压传导);③控制面价目拉取(响应与管理 API 同构,校验落库后原子刷新目录);④`/metrics` 零依赖 Prometheus 文本直写(四类指标,管理 token 门禁,默认关闭)。配置新增 `auth`/`metrics`/`metering.sink`/`control_plane` 四段(muniu.example.yaml 已注释样例)。立法 ADR-0006/0007/0008/0009。遗留:拓扑热替换(providers/model_routes,3.1)与直写 PG 汇后置;测试新增 platform-key/sink/metrics 4 组,`make check` 全绿(18 包+lint 0 issues)。
17. **第二轮审查循环(2026-09-11,P1 变更批)**:三域并行评审报 ~30 项,溯源核验确认 22 项并全部修复——高危:①吊销间隔零值缺省写在构造副本上,NewTicker(0) panic 配 revocations_url 即崩;②缓冲满时 saveBuffered 无限重试阻塞唯一 flushLoop,远端恢复也无法续传(重试前先 best-effort replay 自愈);中危:③ParseMultiplier 整数回绕把非法输入洗成合法倍率;④拉取循环无条件 upsert 致 updated_at churn(内容比对跳过);⑤/metrics 非恒时比较+匿名暴露违背 ADR-0009(改恒时+无 token 不挂载+Recover 包裹);⑥控制台编辑 yaml 行静默丢窗(StaticEntry 如实透出时区/窗);⑦「新增价目」alias 框 disabled 全流程不可用;⑧毒丸行永久阻塞续传(自愈剔除+告警);⑨PUT 双实现校验漂移(收敛 ParsePricingRecord);⑩openapi 响应缺 schema 致 api.ts 手写类型(补 schema 重生成);另修 URL 校验/Close 并发/版本单调防回退/启动即拉/alias trim/bp 值域断言/币种白名单/直方图升序校验/时钟符号宽松等。遗留:拓扑热替换(providers/routes)、直写 PG 汇、P2 参考部署 compose、At 时钟注入。终验:`make check` 全绿。
17b. **第二轮审查循环收敛(2026-09-11 同日)**:评审→修复→复审完成,新增吊销循环回归测试(interval 缺省 panic 钉子/启动即拉/版本单调/Close 幂等),`make check` 全绿(18 包+lint 0 issues+race)。悬置项(下一批次):拓扑热替换 3.1、直写 PG 汇、P2 参考部署 compose、At 时钟注入。
17c. **聚焦复审再修 3 项(2026-09-11)**:①非 200+合法 JSON 时吊销快照 nil err 解引用 panic;②毒丸删除在 MaxOpenConns(1) 下与游标互锁死锁(改为迭代收集+关游标后统一删,新增单连接回归测试);③控制面拉取比对跳过短路刷新重试(pendingRefresh 标记)。顺手:IngestSink 共享客户端、buffer_capacity≥flush_batch 防呆。终验 make check exit=0(18 包+lint 0 issues)。
18. **模型能力目录与请求参数治理(2026-09-11,openspec model-capability-governance,ADR-0010)**:立法 ADR-0010(零内置声明制/逐 attempt 治理/留痕放行+strict 可选),宪法 upstream-adapters.md 增「请求参数治理」节(响应侧白名单不变)。落地:config `models` 段((provider,model) 二元组唯一+引用完整性+值域 fail-fast)与 `strict_governance` 全局开关;`proxy/governance.go` 治理纯函数——Raw 字节层 map 改写(未知字段零丢失)+ 结构字段副本同步(桥接消费 in.MaxTokens/in.Temperature),**原件永不被触碰,每次 attempt 从原始请求按目标 (provider,model) 重新治理**(spec 换道重治理场景);思考锁识别三家关闭思考方言(`enable_thinking:false`/`reasoning_effort:minimal`/`thinking.type:disabled`——协议形态识别,与能力零内置正交);attempt 在 adapter.Prepare 前挂接,strict 命中 400 `param_rejected`(上游零调用);留痕双通道:结构化日志逐条明细(request_id/provider/model/param/action/from/to)+ 响应头 `X-Muniu-Normalized`(换道无干预即清头,零干预请求字节逐字节不变)。测试先行:治理纯函数表驱动 7 组 + 集成钉子 3 组(换道重治理/留痕+出站改写/strict 400),先红后绿;muniu.example.yaml 注释样例;装配层接线(Capabilities/StrictGovernance)。终验:`make check` 全绿(vet+lint 0 issues+web+全包 race)。**代码审查迭代循环(同日)**:双域并行评审 + 聚焦复审两轮收敛,修复 7 项——summarize rune 边界截断、strict 拒绝消息补模型名、drop_params 空白值/协议结构字段(model/messages/stream)校验拒绝、drop 结构字段同步断言、换道后干预头残留断言、日志字段断言、缺 provider/model 校验用例,spec/tasks 措辞对齐实现;3 项疑似经溯源裁决不修(strict 400 不落计量账与 adapter_missing 早退模式一致/治理拒绝记 key EMA 与上游 400 同类语义/缺 provider continue 拆分报错为既有模式)。复审结论:通过,无可检出缺陷;`make check` 复验全绿。
