# Tasks

## 1. 契约与判定表（先 ADR）

- [x] 1.1 补 ADR：错误证据三列判定表与同 provider 换 key 重试；更新 `docs/design/proxy-and-streaming.md` 判定表与 `traffic-governance.md` key 池语义
- [x] 1.2 先写失败测试：`ErrorClass × keySignal/channelSignal/attemptAction` 全格；多 key 同 provider 的 429 重试序列；429 不进渠道窗口；400/404 不动 key EMA

## 2. 实现

- [x] 2.1 failover/proxy：候选循环支持同 provider 换 key（有界、受预算约束），换 key 不计别名换道/渠道窗口
- [x] 2.2 keypool：按表上报，400/404/未知不再写 EMA；key 级硬信号语义不变
- [x] 2.3 health：被动窗口与半开裁决只接收渠道级信号；429/401 不再降渠道

## 3. 验收

- [x] 3.1 `go test ./internal/failover/ ./internal/proxy/ ./internal/keypool/ ./internal/health/ -race` 全绿
- [x] 3.2 `make check` 全绿；`openspec validate error-evidence-scope --strict` 通过
