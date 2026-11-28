# Tasks

## 1. 状态结构与语义（测试先行）

- [x] 1.1 先写失败测试：credit 冷却中的在途 429 只进 rate 桶,credit 仍按原至时刻释放；rate 冷却中的欠费证据不覆盖 rate；两桶并存时 Pick 均不可用；成功只清已到期桶
- [x] 1.2 实现三桶独立截止与释放语义；`go test ./internal/keypool/ -race` 全绿

## 2. 持久化与迁移（测试先行）

- [x] 2.1 先写失败测试：双桶各自落库/恢复；充值只删 credit 行；清除 rate 不删 credit；transient 不落库；迁移后旧行可加载
- [x] 2.2 新增 store 迁移（按 bucket 唯一主键）与 `SaveCooldown/LoadCooldowns` 按桶多行；`go test ./internal/store/ ./internal/keypool/ -race` 全绿
- [x] 2.3 成功路径按桶清除已到期行；启动加载后清理库内已到期行，防长期积累

## 3. 验收

- [x] 3.1 `go test ./internal/keypool/ ./internal/store/ ./cmd/muniu/ -race` 全绿
- [x] 3.2 `make check` 全绿；`openspec validate keypool-cooldown-bucket-isolation --strict` 通过
