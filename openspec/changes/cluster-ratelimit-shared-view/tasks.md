# 任务清单:cluster-ratelimit-shared-view

> 纪律:任务 1(裁决修正)立即执行,不依赖实现;实现任务(2-5)与 platform-data-plane 6.1(cluster 参考部署收尾)联动;standalone 缺省路径零变更,每步 `make check` 全绿。

## 1. 裁决修正(先行)

- [ ] 1.1 ADR-0008 修订:新增「2026-09-13 裁决修正」节——保留 standalone 实例级自治;推翻 cluster 无共享;写明「限流是漏桶不是闸门,需要共享视图,不是强一致」;记录否决方案谱系(全量换 PG:治标错位+毁单二进制+有状态化;维持现状:保护反噬上游+失联矛盾+未贯彻 Nacos 先例;载体预收紧 rpm/N:仅作临时缓解)与业界对照(Kong/APISIX/Envoy/LiteLLM);引本 change 为落地载体。
- [ ] 1.2 `docs/design/cluster-semantics.md` 「实例独立计数 ×N」行更新为目标语义(共享视图消除 ×N;redis 降级退化实例自治并告警),标注「随 cluster-ratelimit-shared-view 落地」。

## 2. 后端抽象(测试先行)

- [ ] 2.1 `internal/ratelimit` 提取 `RateLimitBackend` 接口(After/Allow 判定 + 剩余额度视图),现有进程内实现收编为 `inmem`(缺省);重构后既有全部限流测试不改一行通过(语义零变更证据)。
- [ ] 2.2 冻结限流语义测试集(判定序列 fixture:RPM 令牌桶/TPM 滑窗/日配额、边界与时序),作为两后端对拍基线;回放断言先红后绿。

## 3. redis 后端

- [ ] 3.1 `redis` 后端:原子 INCR + TTL 窗口,key 命名含 key 前缀与窗口号;与 inmem 对冻结测试集对拍,diff 为空。
- [ ] 3.2 降级纪律:超时/不可达回退 inmem 判定 + 持续告警(可观测计数),热路径延迟不受 redis 故障放大(超时预算钉死);恢复自动回共享视图;集成测试覆盖降级与恢复两态。
- [ ] 3.3 配置:cluster 段新增限流后端配置(缺省 inmem;redis 模式填地址);config 聚合校验与文档注释;muniu.example.yaml 补示例。

## 4. 部署与依赖

- [ ] 4.1 `deploy/cluster` compose 增加 redis 服务与 muniu 配置示例(与 6.1 参考部署同一份收尾);standalone 部署文档明确「无需 redis」。
- [ ] 4.2 go.mod 直接依赖 +1(redis 客户端);README「直接依赖」数字与限流小节同步;CI 覆盖 redis 后端路径(容器服务或 miniredis 等价物,保持 CI 零外部服务原则)。

## 5. 收口

- [ ] 5.1 `make check` 全绿 + 语义对拍回放零 diff 留证;HANDOFF 验收记录;ADR-0008 修订注与本 change 互引;归档。
