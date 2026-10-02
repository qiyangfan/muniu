# data-plane-metering-sink Specification

## Purpose
计量汇插拔与本地缓冲续传:计量行是钱,任何形态(单机/集群/断网)下都绝不丢;存储后端可插拔,standalone 保持 SQLite 开箱即用。

## Requirements

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

### Requirement: 接收确认与财务结算分离

计量汇 MUST 将控制面 HTTP 成功响应解释为用量已持久接收；它 MUST NOT 解释为钱包扣款全部完成。控制面对已持久接收但余额不足的用量返回成功时，网关 MUST 结束该批投递；充值后的财务恢复 MUST 由控制面利用同一用量 ID 完成。控制面不能完成持久接收时 MUST 返回失败，网关 MUST 按既有本地缓冲续传契约处理。

#### Scenario: 已接收欠费用量不制造传输重试

- **GIVEN** 控制面已持久保存整批用量，部分行因余额不足进入待结算
- **WHEN** 控制面按既有 ingest 协议返回 2xx
- **THEN** 网关确认该批投递并释放对应缓冲，后续 Mock 充值后的补账由控制面完成
- **AND** 网关不新建用量 ID、不将钱包不足当作网络失败

#### Scenario: 持久接收失败继续保留缓冲

- **GIVEN** 控制面在持久接收一批用量时发生存储失败，即使其中前缀已接收
- **WHEN** 控制面返回非 2xx
- **THEN** 网关保留并按原 ID 续传整批用量，控制面去重已接收前缀，不产生重复计费

#### Scenario: 钱包恢复不改变停服同步协议

- **GIVEN** 控制面在 Mock 入账与待结算恢复后得出了密钥的最终停服状态
- **WHEN** 网关执行既有密钥状态拉取
- **THEN** 网关按返回状态执行调用准入，不自行查询或修改钱包，不要求充值接口等待网关同步
