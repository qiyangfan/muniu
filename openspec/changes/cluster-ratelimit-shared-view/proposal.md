# cluster-ratelimit-shared-view 提案:cluster 形态限流共享视图(对 ADR-0008 的裁决修正)

## Why

2026-09-13 聚合复查对 ADR-0008「实例级自治 + 控制面全局预算纠正,分布式强一致限流明确不做」做了独立重审,结论:**该裁决在 standalone 维度正确,在 cluster 维度把「不做强一致」偷换成了「不做任何共享」,需部分推翻**。三个具体问题:

1. **过载保护反噬被保护对象**。RPM/TPM 副本独立计数意味着 N 副本实际放行 ≈ 上限 ×N(cluster-semantics.md:12 自认)。上游供应商按账号限流——网关对 key 限 60 RPM 实际放行 60×N,打爆的是上游账号配额,随后触发 failover 连锁切换:保护失效不是「弱化」,是让上游与账单直接承压。业界对照:Kong 限流插件(PG/Redis 共享计数)、APISIX(Redis)、Envoy(集中 rate-limit service)、LiteLLM(多副本明确要求 Redis)——「副本各自计数 + 事后纠正」做实时限流没有先例。
2. **与 ADR-0006「宁严勿漏」自相矛盾**。ADR-0006 卖点是「控制面失联仍可鉴权与限额」;但失联时恰恰没有控制面纠正兜底,而本地限额是松弛 ×N 的——最需要严格限额的场景(平台级故障),限额反而最松,日配额失去全局意义。
3. **未贯彻自己引用的先例**。ADR-0005 引 Nacos「单机内嵌 Derby、集群外部 MySQL」——共享状态跟着形态走,正是双形态哲学;该哲学落到了权威状态层,却没落到限流计数层。

同时否决「用 PG 取代 SQLite」的直觉方案:×N 根因是计数器在进程内存,不在本地存储引擎;权威状态的 PG 已按 ADR-0005 正确地放在控制面(锦官),数据面换 PG 治不了 ×N,反而摧毁单二进制叙事并使副本有状态化。

## What Changes

1. **ADR-0008 修订(先行,立即生效)**:记录本次裁决修正——保留「standalone 实例级自治」;推翻「cluster 亦无共享」;写明「限流是漏桶不是闸门,需要的是共享视图,不是强一致」,以及载体预收紧(rpm/N)仅作临时缓解。
2. **限流计数后端可插拔**:提取 `RateLimitBackend` 接口,现有进程内实现更名 `inmem`(缺省,standalone 语义零变更);新增 `redis` 实现(原子 INCR + TTL 窗口,超时/不可达降级回 inmem 并告警——宁有弱限制,勿无限制),仅 cluster 配置启用。
3. **行为等价对拍**:redis 后端与 inmem 后端以同一限流语义测试集对拍(判定结果逐例一致),方法论沿用 053-sql-guard 影子对拍惯例。
4. **cluster 参考部署联动**:`deploy/cluster` compose 增加 redis 服务与配置示例;`cluster-semantics.md` 的「实例独立计数 ×N」行为说明更新为「共享视图,×N 消除;Redis 降级时退化为实例自治并告警」。

## Non-goals

- 不动 mk2 载体语义(载体是限额「声明」,计数后端是「执行」,两者解耦;续签/换发语义不变)。
- 不动计量 sink 与权威状态架构(ADR-0005/0007 维持)。
- 不做集中限流服务(Envoy 模式对个人项目过度设计)、不做强一致(原子 INCR 的最终一致足够,轻微过限可接受)。
- 不改 standalone 缺省路径(不配 redis = 现状逐字节等价)。

## Capabilities

### New Capabilities

- `traffic-governance`:限流语义与计数后端——key 级 RPM/TPM/日配额的执行纪律、计数后端可插拔(inmem/redis)、降级语义与行为等价要求(主 specs 尚无此 capability,本变更建立)。

## Impact

- 代码:`internal/ratelimit/`(后端接口化 + redis 实现)、`internal/config/`(cluster 限流后端配置段)、`cmd/muniu/`(装配分支)。
- 新增依赖:redis 客户端库(仅 redis 后端路径使用;go.mod 直接依赖 +1,README「直接依赖」数字同步)。
- 部署:`deploy/cluster/`(compose + redis 服务示例,与 platform-data-plane 6.1 收尾联动)。
- 文档:ADR-0008 修订、`docs/design/cluster-semantics.md`、README。
- 实现时机:任务 1(ADR 修订)立即执行;任务 2-5 与 cluster 形态收尾(platform-data-plane 6.1)联动推进——先把裁决改对,避免「ADR-0008 已裁决」成为不做的挡箭牌。
