# 单二进制取舍:纯 Go SQLite 与零外联

> muniu 设计复盘系列 ④。对应实现:`internal/store`(modernc.org/sqlite)、`internal/metering`(tiktoken 离线词表)、`web/`(go:embed 控制台)。选型决策:`docs/adr/0001-go-single-binary-embed-sqlite.md`、ADR-0002。

## 为什么执着于「一个文件」

LLM 网关这类基础组件,部署摩擦每增加一分,采用率就掉一分。muniu 的目标形态是:`CGO_ENABLED=0` 交叉编译出单文件,`./muniu` 起服,**零 compose、零外部数据库、零运行时下载**。这个目标约束了三个技术选择,每一个都有代价,本文如实记录代价。

## 取舍一:modernc.org/sqlite(纯 Go 转译)而不是 CGO mattn

标准做法是 mattn/go-sqlite3,但要 CGO——`CGO_ENABLED=0` 的纯静态交叉编译直接出局。muniu 选 modernc:SQLite C 代码机器转译成 Go,慢一些(单机网关场景读写都轻,计量落库是批量异步的,延迟不敏感),换来的是:

- darwin/linux × amd64/arm64 交叉编译一条命令,无需任何 C 工具链;
- 压测 `BenchmarkRateLimitAllow` 184ns/op、计量批量落库在压测档位下无瓶颈(详见 Phase 4 验收记录)。

代价:二进制大(modernc 转译层不小)、极端写吞吐低于 CGO 版。对「单机 sidecar、量级到万级日请求」的定位,这笔交易是赚的。

## 取舍二:tiktoken 词表编译期嵌入

语义缓存的本地估算依赖 tiktoken 分词。官方词表托管在 OpenAI CDN,常规做法是运行时下载——但 muniu 的部署纪律是**零外联**(网关是流量咽喉,任何运行时外联都是可用性与合规的软肋)。方案:`tiktoken-go-loader` 的 OfflineLoader 把词表编进二进制,`init()` 里一行接线,之后 BPE 编码纯属内存计算。

代价:二进制再大几 MB;词表更新需要重新发版。对缓存估算(允许 ±10% 误差、置 `estimated` 供对账剔除)来说,完全可接受。

## 取舍三:控制台 go:embed 进二进制

管理界面(React 19 + Vite + Tailwind v4)构建产物 `web/dist` 经 `go:embed all:dist` 进二进制,`/console/` 路由直接服务。前端有独立的 `pnpm typecheck && pnpm lint`(纳入 `make check`,pnpm 存在才执行),`openapi-typescript` 从契约单源生成 API 类型——**API 类型禁止手写**,漂移由 `contracts/openapi.yaml` 与路由注册集合的双向比对测试钉死(`TestRoutes_与OpenAPI契约无漂移`,这个测试抓到过真实的漏注册)。

代价:`.gitkeep` 占位配合 embed 的模式稍绕;前端改一行要重跑构建才能在二进制里生效。

## 反面:什么时候不该这么干

单二进制不是免费午餐。以下场景建议直接放弃这个目标:

- 需要 HA 多实例与分布式状态(单二进制定位即单机,muniu 把 Redis 分布式限流、集群形态明确放进 backlog);
- 数据规模超出单机 SQLite 舒适区(万级日请求的 usage_logs 完全没问题,亿级另说);
- 团队需要严格的进程隔离与独立伸缩。

## 数字

- `CGO_ENABLED=0 go build` 产物单文件,`/healthz`、`/v1/chat/completions`、`/v1/embeddings`、`/admin/v1/*`、`/console/` 全在其中;
- SSE 转发增量开销 6μs/chunk(`BenchmarkSSEForward`,1000 事件);
- 计价 0.57ns/op、限流判定 184ns/op、零堆分配(纯整数运算的直接回报);
- 迁移 0001-0003 embed 进二进制,启动时单事务顺序执行,幂等可重入。

单二进制是一种纪律,不是一种炫技:它强迫每个依赖回答「你值得让用户多携带多少 MB、多一步安装吗」。大多数时候,答案是裁掉。
