# 流量治理:key 池 · Failover 迭代 · 限流 · 配额(traffic-governance)

## key 池(Phase 3)

每 provider 挂多把 key(`muniu.yaml` 只存环境变量引用名)。三态:

| 状态 | 进入条件 | 行为 | 退出 |
|---|---|---|---|
| 健康 | 默认 | 参与选择 | 命中下表任一冷却 |
| 冷却 | 429(Retry-After,缺省 60s,指数 ×2,上限 30min)/ 5xx(连续 3 次,30s) | 跳过选择;冷却自然到期恢复 | 到期 |
| 禁用 | 401/403 | 跳过选择并 warn 日志(人工介入级) | 仅进程重启/改配置 |

**选择算法:平滑加权轮询(smooth WRR)**,同 priority 内按 weight 分配;健康度系数 = 最近 20 次结果 EMA 成功率,`有效权重 = weight × max(0.1, 成功率)`(禁用阈值 0.1 防饿死)。选择在互斥锁内完成,复杂度 O(n)。

## 候选迭代器(failover 的驱动器)

`internal/failover`:按 model_alias 取候选列表 → **同 priority 加权随机选一起点,失败向下一 priority 线性降级**(语义见 proxy-and-streaming 判定表)。迭代器是有界纯函数(输入候选表+随机源,输出序列),零 IO,可穷举测试。

## 限流(维度:per apikey;Phase 3 起另加 per tenant 聚合档)

| 机制 | 实现 | 语义 |
|---|---|---|
| RPM 令牌桶 | `golang.org/x/time/rate`,桶容=N,速率=N/min | 硬闸,拒绝即 429 + `Retry-After`(Phase 2) |
| TPM 滑动窗口 | 60×1s 环形桶,计数**已完成请求的 token 数** | 硬闸:窗口总量 ≥ tpm_limit 时拒绝新请求(Phase 3)。固有近似:在途请求 token 未计,误差 ≤ 单窗口并发量,如实写进文档不做伪精确 |
| 日配额 | 已耗成本(credit,1 credit = 本位币 1e-9,见 metering)≥ daily_quota_credits → 402 | 见下「一致性声明」 |

**配额一致性声明(契约)**:配额/TPM 均为**事后记账**模型——请求进行时无法预知最终 token 数,因此存在「同刻并发小幅超限」窗口。muniu 的取舍:**宁可记账精确、偶发小额超限,不做预留式伪精确**(预留模型复杂度不成比例,且 token 上限本就未知)。该取舍写入 README FAQ,面试可直接讲。

分布式形态(Redis lua 原子窗口)在 backlog,单二进制优先;演进路径:限流器抽 `Limiter` 接口,Redis 实现作为同接口的另一实现接入,请求路径零改动。

## 多租户(数据模型)

```
tenants(id, name, status)          ← Phase 2 建,default 租户随迁移种子生成
api_keys(tenant_id, rpm/tpm/quota 限额列, ...)  ← 限额挂在 key 上
```

v0.2 起租户聚合限额生效(该租户全部 key 共享 TPM/日配额第二道闸);租户间无资源隔离(单进程共享吞吐,排队按到达序)——如实定位:**多租户是计量与配额的边界,不是性能隔离的边界**。

## 测试要求

- key 池:WRR 分布均匀性(统计断言)、冷却进出状态机、EMA 权重衰减;
- 限流:令牌桶边界(恰 N-1/N/N+1)、TPM 环形桶跨窗滑动、配额边界(恰差 1 微单位);
- 全部并发对抗 `-race`。
