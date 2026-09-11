# Phase 3:流量治理(v0.2.0)

> 状态:代码与验证完成(2026-09-10;v0.2.0 tag 待用户指令) · 前置:Phase 2 闸门全绿 · 版本:**v0.2.0** · 允许新增依赖:`golang.org/x/sync`(errgroup,仅测试/演练编排需要时)

## 目标

网关「可信」:key 池三态与加权调度、failover 完整语义(首 token 边界)、TPM 滑动窗口硬闸、日配额与租户聚合闸、国产五家 quirk 补齐、Anthropic 协议桥接;并以可重复的故障演练证明这些语义。

## 任务清单

- [x] **T3.1 key 池(测试先行)**:`internal/keypool`:每 provider 多 key;平滑加权轮询(WRR);三态状态机(健康/冷却/禁用,进入退出条件按 traffic-governance 表:429→Retry-After 或 60s 指数×2 上限 30min;5xx 连续 3 次→30s;401/403→禁用+warn);EMA(最近 20 次)健康度 × 权重,下限 0.1。测试:WRR 分布统计(10k 次抽样 ±5%)、状态机全迁移、并发 `-race`。
- [x] **T3.2 failover 纯函数层(测试先行)**:`internal/failover`:`ErrorClass` 枚举完整化(dial_fail/timeout_first/upstream_5xx/rate_limited_429/auth_failed/bad_request_400/mid_stream_fail/context_canceled);`Decide(class, pastFirstToken, stream) Action`(换道/断言/直接返回/静默);候选迭代器(同 priority 加权随机起点、priority 递增降级)。**判定表逐格表驱动测试,一格一用例,用例名含格坐标**。
- [x] **T3.3 proxy failover 循环**:实现生命周期第 9 步:取候选→取 key→执行→按 `Decide` 动作分支;首 token 边界由 adapter `StreamEvent` meta「含实质内容」置位;断言出口 = 流内错误帧 + `data: [DONE]`;`context_canceled` 静默且计量已发生部分;失败结果回写 key 池(冷却/禁用/EMA)。fake 剧本补 `stream_429 / stream_5xx_after_first_token / client_disconnect`;每剧本三项断言(客户端字节序列 / usage 行 / key 池状态)。
- [x] **T3.4 TPM 滑动窗口(测试先行)**:`internal/ratelimit`:每 key 60×1s 环形桶,请求完成后记账 token 数;新请求前窗口总量 ≥ tpm_limit → 429;`tpm_limit=0` 不限。测试:跨窗滑动、边界、并发。文档「固有近似」声明落 README FAQ。
- [x] **T3.5 日配额与租户聚合闸(测试先行)**:quota 中间件从占位变真:`SumCostToday`(estimated=0)进程内缓存 30s → ≥ `daily_quota_micro` → 402;租户聚合(该租户全部 key 之和)第二道闸(迁移 0002:`tenants` 加 `daily_quota_micro/tpm_limit` 列,**新迁移文件,不改 0001**)。测试:边界恰差 1μ、缓存过期后生效。
- [x] **T3.6 缓存价目**:pricing 增 `cached_input`(缺省=input);DeepSeek 缓存命中价、OpenAI cached 折扣接入计价;fixture 补 usage 缓存字段场景;`muniu.example.yaml` 同步。
- [x] **T3.7 quirks 补齐五家**:`qwen.go`(DashScope 兼容模式)、`doubao.go`(Arc 端点 ID 即 model);先官方文档核对 → fixture → 实现;矩阵 ⚠ 清零。
- [x] **T3.8 Anthropic 桥接**:`adapter/bridge/anthropic.go` + `bridge/README.md`(翻译矩阵:messages/system 提升、tools→tool_use、stop_reason 映射、SSE 事件机 `message_start/content_block_delta/...` → OpenAI chunk、usage `input/output/cache_read`);fixture ≥3(纯文本流/工具调用流/thinking);Classify 按 Anthropic 错误体。这是唯一允许改写客户端可见字段的适配器。
- [x] **T3.9 故障演练(chaos)**:`internal/proxy/chaos_test.go`(build tag `chaos`,`make chaos` 目标加入 Makefile):剧本「provider A 全部 key 连续 429 → 冷却 → 降级 priority 1 的 B 成功 → A 冷却到期恢复被重新选中」,断言事件序列与日志;真上游演练:故意配一把坏 key,观察换道日志,脱敏实录。
- [x] **T3.10 文档与发版**:README 增「failover 语义」「配额一致性 FAQ」章节;HANDOFF 更新;`v0.2.0` tag/push 待用户指令。

## 设计指针

`traffic-governance.md` 全文 · `proxy-and-streaming.md`「首 token 边界」「判定表」「流内错误帧」 · `upstream-adapters.md`「桥接」 · `metering.md`「缓存价」。

## 验收闸门

1. 判定表全格用例列表(测试名可数)绿;2. chaos 剧本绿 + 真上游换道实录;3. conformance 覆盖国产五家 + Anthropic ≥3;4. TPM/配额边界测试绿;5. `make check` 全绿;6. 矩阵 ⚠ 清零。

## Non-goals

Redis 分布式限流 · Gemini 桥接(Phase 4 可选)· 管理 API/控制台 · 语义缓存 · 热加载。
