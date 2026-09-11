# ADR-0001:Go + 单二进制 + 纯 Go SQLite(CGO_ENABLED=0)

- 状态:已接受(2026-09-08)
- 背景人:muniu 定位为简历级 Go 工程作品与 kongming 生产基建,分发自托管开发者。

## 决策

1. 语言 Go(go 1.25+,go.mod 锁定);2. 产物为单静态二进制;3. 存储 SQLite 经 `modernc.org/sqlite`(纯 Go 翻译驱动),构建锁定 `CGO_ENABLED=0`;4. tiktoken 词表等资产编译期 embed,运行时零下载。

## 理由

- Go 是该赛道生态主场(one-api/Bifrost/BricksLLM 皆 Go),并发模型(SSE 长连接 × 万级并发)与部署形态契合;
- 纯 Go 驱动是单二进制的**前提**:`mattn/go-sqlite3` 需 cgo,交叉编译矩阵(darwin/linux × amd64/arm64)立即碎裂。代价是 modernc 比 mattn 慢(量级 ~1.5-2×),但网关的 SQLite 负载是低频写(异步批量 usage)+ 点查(apikey),瓶颈根本不在 DB——为不存在的瓶颈放弃交叉编译才是错误;
- 运行时零下载(词表 embed)与「配置错即启动失败」同源:**单二进制的品格 = 零外联、零意外**。

## 备选与否决

- Rust:性能更强,但 3-6 个月学习曲线与 side project 时间预算冲突,项目大概率烂尾(2026-09-08 定调,见仓库外决策记录);
- Postgres 必需(one-api 形态):分发体验断崖,sidecar 定位不成立;
- 嵌入式 KV(BoltDB 等):需聚合查询(日配额/账单),SQL 不可替代。

## 后果

写吞吐受 SQLite 单写者限制 → 计量异步批量(metering)成为必然设计;若未来出现写瓶颈,演进方向是可选外置 Postgres(同 SQL 接口),而非现在预建。
