# Proposal

## Why

chat/messages 入站零会话概念:同一会话逐请求独立 smooth WRR——把已建立上游前缀缓存的会话随机甩到别的 key 上,缓存全部 miss。上游 prompt cache(DeepSeek/Kimi/GLM/Anthropic 等,cache 读计价远低于全价输入)是 Agent 多轮流量最大的成本杠杆,而调度自己在拆它:Claude Code 直连 `/v1/messages` 是一等场景,会话动辄数十轮,每轮换道的期望缓存损失随轮深线性放大。

现状:亲和表(`internal/proxy/affinity.go`)仅服务 responses 状态亲和(`previous_response_id`,功能必需非成本优化);chat/anthropic 两条 lane 无任何会话粘性。

竞品对照(magpie,2026-11-03 考察):会话粘住 + worth 闸门(cache 读写>0 才值得粘)+ 失败即清——思想可移植,身份解析按服务端多租户重定(不信任客户端钉账号)。

**路由行为可观测变化**(同会话稳定命中同渠道/key,除非健康不可用)→ 按项目纪律先立 **ADR-0020**。

## What Changes

- **会话缓存亲和**(独立于 responses 状态亲和的新表):会话身份 = 请求头 `X-Session-Id`(可选,租户自报)> `apiKeyID + SHA-256(首条 user 消息文本)`;无首条 user 消息的请求不参与亲和。
- **归属隔离**:亲和条目绑 `apiKeyID`(沿用 responses-state-ownership 先例)——自报头只影响自己 key 名下的分组,跨 key 不可见,无越权面。
- **worth 闸门**:仅当某 (渠道, key) 上次成功 attempt 的 usage 上报缓存 token 读写 >0 才建立粘性——无缓存证据不粘,避免把会话粘到无前缀缓存的慢源上。
- **健康前提重排**:候选循环前查表——stick 存在、未过期、stick 候选健康(未冷却/未禁用/未欠费、渠道未熔断)→ 置于候选序最前(池选择同 key 优先);stick 候选因冷却/熔断暂不可用 → 按原序、条目保留(恢复即复粘);**本次尝试失败或换道 → 立即清除条目**(该 key 对该会话的缓存前提已破)。池冷却/渠道熔断恒优先于亲和;亲和零新增重试。
- **lane 边界**:作用于 `/v1/chat/completions` 与 `/v1/messages`;responses lane 维持既有状态亲和不动;gemini/images/embeddings 不适用。
- **语义缓存互斥**:命中语义缓存即不出站,亲和表零读写;亲和只作用于缓存 miss 后的出站路由。
- 表容量 LRU 上限 + TTL 10 分钟(固定值起步,per-vendor 冷却时长不进本期),进程内存态不落盘;metrics `muniu_cache_affinity_total{result=hit|miss|stale}`。

## Capabilities

### New Capabilities

- `session-cache-affinity`:会话身份解析与归属隔离、worth 闸门、健康前提重排与失败即清、可观测与容量纪律。

### Modified Capabilities

(无——`upstream-health` 的熔断收缩发生在候选循环内,亲和重排发生在收缩之后,优先级由新能力自述,与既有规格不冲突)

## Impact

- `internal/proxy`:亲和表扩展(新表,不动 responses 表)、身份解析、chat/messages 两 lane 候选重排接线。
- `internal/keypool`:带提示的选中 API(如 `PickWithHint`,`nil`/未命中走既有 smooth WRR,缺省行为不变)。
- metrics 面:`muniu_cache_affinity_total`。
- `docs/adr/0020-session-cache-affinity.md`(路由行为变化记录);`docs/design/proxy-and-streaming.md` 亲和节补记。
- 零新增依赖;零配置面(行为缺省启用,TTL/LRU 为常量;如需开关另立裁决)。
