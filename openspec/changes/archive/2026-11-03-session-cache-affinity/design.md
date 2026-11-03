# Design

## 结构决策

**新表,不动 responses 亲和表**:responses 状态亲和是功能必需(`previous_response_id` 指向上游有状态存储),语义是「必须回原渠道」;缓存亲和是成本优化,语义是「健康前提下优先」。两者生命周期/淘汰/清空条件完全不同,共享数据结构只会互相拖累。

**身份解析**:`X-Session-Id` 为租户可选自报(kongming 等平台调用方可显式传,消除哈希歧义);缺省哈希输入 = `apiKeyID ‖ 首条 role=user 消息文本`。取首条 **user** 而非 system:Agent 客户端中途插改 system(TodoWrite 提醒、压缩摘要等)是常态,首条 user 消息跨轮不变,身份稳定;多模态数组取文本段按序拼接后哈希,截 12 字节。解析在入站 lane 各做一次,请求期 O(1)。

**worth 闸门的数据来源**:attempt 完成上报 usage 处(cache token 字段计费链已在用)顺带更新亲和表——缓存读 >0 时记(写侧/缓存建立的各方言归一不一,v1 不参与判定——与 docs/design/proxy-and-streaming.md 亲和节同口径) `会话 → (渠道, key)`;未命中或零值时**不清除**已有条目(本轮经别家回答不代表条目失效——失败才清;零缓存证据只是不新建/不续期)。

**重排与池提示**:候选循环内,stick 渠道健康时排至候选序最前;池侧新增 `PickWithHint(provider, keyID)`——hint key 健康(非冷却/禁用/欠费)直接返回,否则退回 smooth WRR;hint 为 nil 或 key 不存在走既有路径,缺省行为逐字节不变。

**TTL/LRU**:TTL 10 分钟固定常量(magpie 的 per-vendor 冷却时长=按厂商实测的精调项,本期不做配置面,记 design「后续可调」);容量对齐 responses 亲和表先例(4096 LRU)。进程内存态,重启归零由 worth 闸门自然重建,不落盘。

## 与既有语义的优先级链

请求 lane 顺序(既有):auth → quota → ratelimit → 语义缓存 → 候选循环。亲和插在语义缓存 miss 之后、候选循环之前:

1. 熔断收缩(upstream-health,既有)先砍掉降级渠道;
2. 亲和查表:hit 且 stick 存活 → 候选重排 + 池 hint;stale → 计数后原序;
3. 换道语义(failover)不变——stick 候选失败照常换道,换道即清条。

优先级不变式:**熔断 > 池冷却/禁用/欠费 > 亲和 > WRR**。亲和永不增加候选、永不重试、永不覆盖任何健康判定。

## lane 与交互边界

| lane | 行为 |
|---|---|
| `/v1/chat/completions` | 接入 |
| `/v1/messages`(anthropic 入站) | 接入(翻译后 canonical chat 上同一亲和点) |
| `/v1/responses` | 不动(既有状态亲和) |
| gemini/images/embeddings | 不接入(无多轮会话语义或缓存收益结构不同) |
| 语义缓存命中 | 零读写亲和 |

## 测试策略

- 身份稳定性失败测试先行:同会话注入 system 噪声/工具提醒 → 同 stick;换 apiKey → 不可见;无 user 消息 → 零读写(先红后绿)。
- worth 闸门:cache 读写>0 建粘、=0 不建、失败即清、冷却让位保留条目、恢复复粘(先红后绿)。
- 端到端:双候选渠道 + 伪上游(统计命中渠道),断言同会话连续 N 轮粘同一候选;stick 候选 429 冷却后按原序且恢复后复粘。
- 突变验证:破坏 worth 闸门(去掉 >0 判定)与破坏失败即清各一处,测试必须变红。

## 后续可调(本期不做)

per-vendor TTL(DeepSeek 1h 缓存 vs 缺省 5 分钟冷却是 magpie 实测参数)——等真实缓存命中率数据再定,避免无证据的配置面;`X-Session-Id` 的管理面透传规范(kongming 侧)跨仓另行登记。
