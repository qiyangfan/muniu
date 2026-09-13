# 设计说明:平台化数据面(ADR-0005 落地)

## Context

终局目标为公开注册、真实计费、HA 的平台。ADR-0005 已裁决:muniu = 数据面,双形态;控制面(用户/钱包/支付/门户)为独立产品。本变更执行其在本仓的立法与演进。证据基线(Envoy+xDS、Kong DB-less、Nacos 双形态、Grafana、Gitea 反例、one-api/LiteLLM/Portkey/Helicone)见 ADR-0005。

## Goals / Non-Goals

- Goals:副本无状态化;standalone 零回归;计量行任何形态不丢;控制面失联可降级服务;可观测。
- Non-Goals:控制面产品本身(另立仓库);分布式强一致限流(v1 用「实例级自治 + 控制面全局预算」);muniu 内嵌 PG 驱动为默认依赖;真实支付 PSP 接入。

## Decisions

- **定位:数据面,双形态**(ADR-0005)。否决单体平台化——爆炸半径、安全面、HA 正确姿势、宪法连续性四理由,详见 ADR。
- **是否切 PG:控制面切,muniu 不切。** 控制面第一天起 PG 权威;muniu standalone 保持 SQLite(开箱形态),cluster 下 SQLite 降级本地缓冲兜底;直写 PG 汇为可选实现且需依赖白名单 ADR。理由:PG 是集群的**前提**而非集群本身——先切 PG 而不做无状态化,得到的只是三个互不认识的单机加一份新运维负担。
- **支付:不真实落地,预留抽象接线。** 账本只认入账事件;`PaymentProvider` 抽象(CreateOrder/VerifyWebhook/QueryOrder)在控制面定义,MVP 管理员手工入账走同一事件通道天然幂等;真实 PSP = 未来一个 adapter;不做未经真实验证的支付代码(契约测试 + mock provider 证明接线)。
- **自校验 key 优先于快照同步做主路径**:HMAC 签名使副本零查询验真,吊销经版本同步收敛(有界窗口)——把「控制面不可达」从鉴权故障降级为吊销延迟问题;查库 key 保留,standalone 零回归。
- **配置源与计量汇都走「目录/汇」抽象**:复用 `PricingDirectory` 已验证的原子热替换模式;计量汇以「缓冲续传 + 幂等」为硬契约,本地 SQLite 是所有形态的最后兜底(计量行是钱)。

## Risks / Trade-offs

- 平台 key 的吊销窗口(同步周期)内有残留放行 → 窗口上界可配置并文档化默认值;高危场景可即时递增全局版本强制收敛。
- metrics client 引入新依赖 → 白名单制 ADR 增补,优先评估文本协议直写(零依赖)再评估 client 库。
- 跨仓契约漂移(key 格式/同步 API/ingest 协议)→ 双方以契约测试钉死,本仓 conformance 纪律延伸。

## Migration Plan

P1 按 state-sync → metering-sink 顺序落地,每步 standalone 形态回归等价(现网 kongming 部署零感知);P2 出 cluster 参考部署;控制面产品独立排期,不阻塞本仓演进。

## Open Questions

无(支付与 PG 两处已由用户裁决,记录于 proposal)。
