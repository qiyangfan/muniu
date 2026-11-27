# Tasks

## 1. 取样与视图（测试先行）

- [ ] 1.1 先写失败测试：首键 disabled 时探针改用下一可用 key；首键冷却同理；全部不可用时零探针请求、零失败计数；连续两轮探针覆盖不同 key（轮换）；`go test ./internal/health/ -run TestManager_探针取样` 先红
- [ ] 1.2 实现 `keypool.ProbeKeys(now)` 只读视图与 health 轮换取样；断言 Pick/Report 零调用、key 状态零变化

## 2. 验收

- [ ] 2.1 `go test ./internal/health/ ./internal/keypool/ -race` 全绿
- [ ] 2.2 `make check` 全绿；`openspec validate health-probe-integrity --strict` 通过
