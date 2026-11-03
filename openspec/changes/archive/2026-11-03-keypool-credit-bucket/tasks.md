# Tasks

## 1. 状态机(测试先行)

- [x] 1.1 失败测试先行:欠费桶判定与阶梯——402 进桶 30min、连续欠费 30m→1h→2h 封顶、成功清零、`OutcomeCreditExhausted` 期间 Pick 不选中(先红后绿)
- [x] 1.2 `internal/keypool`:枚举追加、欠费冷却状态机、与 429/5xx 桶互斥共存的回归(既有状态机测试全绿)

## 2. 欠费证据上报(测试先行)

- [x] 2.1 失败测试先行:白名单表驱动——402 命中、`insufficient_quota` 错误码命中、文案含「余额」但白名单外不命中、白名单外 429 维持限流桶(先红后绿)
- [x] 2.2 proxy 上报 lane:响应证据(状态码+错误体错误码)→ `OutcomeCreditExhausted` 映射;quirk 层与 `ErrorClass` 零改动回归

## 3. 持久化(测试先行)

- [x] 3.1 迁移 0018 冷却表(schema + 迁移纪律:应用后禁改)
- [x] 3.2 失败测试先行:≥5min 落库/30s 不落库、重启加载恢复(至时刻+阶梯)、SIGHUP 对账(键在保留/键删清行)、脏刷写失败续传(先红后绿)
- [x] 3.3 `internal/keypool` 池锁外刷写接入(脏标记 + 500ms 批量 UPSERT),启动装配加载

## 4. 余额回写(测试先行)

- [x] 4.1 失败测试先行:balance Service 逐 key 源回调触发、账户级源不回调、nil 回调零开销(先红后绿)
- [x] 4.2 失败测试先行:回写幂等(重复评估不推进阶梯/不刷新至时刻)、余额>0 提前释放并清零阶梯、查询失败不回调维持状态(先红后绿)
- [x] 4.3 `cmd/muniu` 装配接线 + metrics `muniu_keypool_credit_events_total{cause}` 与提前释放计数

## 5. 文档与收尾

- [x] 5.1 `docs/design/proxy-and-streaming.md` 池语义节补记(欠费桶/白名单/持久化/回写)
- [x] 5.2 突变验证:破坏白名单封闭与 2h 封顶各一处,测试变红后还原复绿
- [x] 5.3 `make check` 全绿;`openspec validate --all --strict` 通过;HANDOFF 验收记录登记
