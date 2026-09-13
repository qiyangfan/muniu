# 平台化:数据面双形态与控制面边界(ADR-0005 落地路线)

## Why

终局目标是公开注册、真实计费收款、高可用的平台型产品。经 ADR-0005 裁决(2026-09-11,已接受):muniu 定位为 **LLM 网关数据面**,不做单体平台转型——平台能力(用户/钱包/支付/门户)归独立控制面产品(新仓库,PostgreSQL 为权威存储);muniu 演进为双形态数据面(standalone 默认 / cluster 无状态副本),为 HA 铺路的同时保住「单二进制零依赖」的采用门槛。本变更承载该裁决在本仓的立法与演进任务,并记录支付与存储两处范围裁决。

## What Changes

- **定位与边界(立法,已生效)**:宪法非目标修订——「高可用集群形态」移出非目标(集群数据面按本变更演进);商业化与用户门户仍不在本仓(归控制面产品)。证据基线与被否决备选见 `docs/adr/0005-product-positioning-data-plane.md`。
- **存储形态裁决**:控制面产品从第一天起以 PostgreSQL 为权威存储;muniu **不切 PG**——standalone 保持 SQLite(默认一等形态),cluster 形态下 SQLite 降级为本地缓冲与审计兜底;计量汇可插拔(ADR-0007 立项)。
- **支付范围裁决**:支付不真实落地,控制面预留 `PaymentProvider` 抽象接线(账本只认入账事件,MVP 以管理员手工入账兜底);真实 PSP 接入 = 未来实现一个 provider adapter;不做未经真实验证的支付代码。支付属控制面能力,不建 muniu capability spec。
- **P1 状态分层(本仓实现项)**:①自校验 API key(HMAC 签名,网关离线验真,吊销经版本同步收敛),与现有查库 key 并存;②配置源抽象——providers/model_routes/pricing 由「文件(现状)或控制面版本化拉取」双驱动,复用 `PricingDirectory` 原子热替换模式;③计量汇插拔(本地 SQLite / 控制面 ingest / 直写 PG),本地 SQLite 永远兜底缓冲。
- **P1.5 可观测**:`/metrics` Prometheus 文本协议端点(client 依赖按白名单制 ADR 增补)。
- **P2 参考部署**:cluster 形态 compose/k8s manifests 与部署文档(多副本 + LB + 控制面对接)。

## Capabilities

### New Capabilities

- `data-plane-state-sync`: 无状态副本的状态分层——自校验 key 验证、配置源双驱动与原子热替换、回落语义。
- `data-plane-metering-sink`: 计量汇插拔与本地缓冲续传(计量行绝不丢)。
- `data-plane-observability`: Prometheus 指标端点(转发吞吐/上游状态/计量管道水位)。

### Modified Capabilities

(无既有能力的需求变更。)

## Impact

- 本仓:`internal/auth`(key 验证)、`internal/proxy`+`cmd/muniu`(配置源与计量汇接线)、`internal/metering`(汇接口)、`internal/server`(metrics 端点)、参考部署目录、依赖白名单(metrics client ADR 增补)。
- 控制面产品(范围外,另立仓库):用户体系、钱包账本、`PaymentProvider` 抽象、渠道与价目下发 API、PG 权威存储;引用 ADR-0005 作为边界依据。
- 跨仓契约(须双方钉死):自校验 key 格式、配置同步 API(版本化拉取)、计量汇 ingest 协议。
