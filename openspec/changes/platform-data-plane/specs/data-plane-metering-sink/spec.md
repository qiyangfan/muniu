## Purpose

计量汇插拔与本地缓冲续传:计量行是钱,任何形态(单机/集群/断网)下都绝不丢;存储后端可插拔,standalone 保持 SQLite 开箱即用。

## ADDED Requirements

### Requirement: 计量汇可插拔

计量管道的落库端必须 (MUST) 可插拔:本地 SQLite(standalone 默认)、控制面 ingest 接口、直写 PostgreSQL(cluster 可选);汇实现必须 (MUST) 满足同一契约——批量写入、幂等(request_id/行主键去重)、背压优先于丢单(channel 满 block 并告警,绝不 drop)。

#### Scenario: 汇切换行为不变
- **WHEN** 同一请求序列分别经本地 SQLite 汇与控制面 ingest 汇落账
- **THEN** 两边得到的计量行集合逐字段等价(金额/token/来源/幂等键)

### Requirement: 本地缓冲续传

cluster 形态下控制面或网络不可达时,计量行必须 (MUST) 落入本地 SQLite 缓冲队列并按容量上限保留(超限必须 (MUST) 阻塞请求路径并告警,与背压纪律一致);恢复可达后必须 (MUST) 按序续传且幂等去重,续传完成前本地缓冲必须 (MUST) 可查询(对账兜底)。

#### Scenario: 断网缓冲续传
- **WHEN** 控制面不可达期间产生 N 行计量,随后恢复可达
- **THEN** N 行全部经幂等去重后进入权威存储,无丢失、无重复计费

#### Scenario: 缓冲容量上限
- **WHEN** 缓冲队列达到容量上限且下游仍不可达
- **THEN** 请求路径阻塞并持续告警(与「计量行是钱」背压契约一致),不静默丢弃

### Requirement: 存储形态裁决

standalone 形态必须 (MUST) 以 SQLite 为唯一持久层且为默认开箱形态;cluster 形态的权威存储为控制面 PostgreSQL,muniu 不内嵌 PG 驱动为默认依赖(如需直写 PG 汇,经依赖白名单 ADR 增补);任何形态下本地 SQLite 缓冲文件必须 (MUST) 保持可独立对账。

#### Scenario: 独立部署开箱形态
- **WHEN** 以 standalone 形态启动(未配置任何平台接入)
- **THEN** 计量落库走本地 SQLite,不引入 PG/Redis 等外部依赖,行为与历史版本一致
