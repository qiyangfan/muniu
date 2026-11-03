# Tasks

## 0. ADR 先行(路由行为变化纪律)

- [x] 0.1 `docs/adr/0020-session-cache-affinity.md`:记录「同会话健康前提下稳定命中同渠道/key」的路由行为变化、worth 闸门依据、与 responses 状态亲和/熔断/欠费桶的优先级链

## 1. 亲和表与身份解析(测试先行)

- [x] 1.1 失败测试先行:身份解析三叉——`X-Session-Id` 优先、消息哈希兜底(注入 system 噪声身份不变)、无 user 消息零参与(先红后绿)
- [x] 1.2 失败测试先行:归属隔离(跨 apiKeyID 不可见)、TTL 过期、LRU 容量淘汰(先红后绿)
- [x] 1.3 `internal/proxy`:新亲和表(独立于 responses 表)+ 身份解析,两 lane(chat/messages)接入点

## 2. worth 闸门与池提示(测试先行)

- [x] 2.1 失败测试先行:cache 读写>0 建粘/续期、=0 不建不清、失败即清、冷却让位保留条目恢复复粘(先红后绿)
- [x] 2.2 `internal/keypool`:`PickWithHint`(hint 健康直返,否则 smooth WRR;nil/未知 key 逐字节等价既有路径)+ usage 上报点顺带更新亲和表

## 3. lane 接线与可观测

- [x] 3.1 chat/messages 两 lane:语义缓存 miss 后亲和查表 → 候选重排 + 池 hint;responses/gemini/images/embeddings 零触碰回归
- [x] 3.2 metrics `muniu_cache_affinity_total{result}` 三分计数(hit/miss/stale 互斥且覆盖)

## 4. 文档与收尾

- [x] 4.1 `docs/design/proxy-and-streaming.md` 亲和节补记(优先级链/lane 边界/后续可调)
- [x] 4.2 突变验证:破坏 worth 闸门与失败即清各一处,测试变红后还原复绿
- [x] 4.3 `make check` 全绿;`openspec validate --all --strict` 通过;HANDOFF 验收记录登记
