# Tasks

## 1. 契约与测试（先确认）

- [ ] 1.1 确认 429 `tenant_inflight_exceeded` 契约与默认 0 语义；更新 traffic-governance 文档与 example
- [ ] 1.2 先写失败测试：租户 A 占满后 A 新请求 429、B 正常；槽位释放后 A 恢复；断连/预算中断释放；0=不限；`go test ./internal/proxy/ -run TestTenantInflight` 先红

## 2. 实现

- [ ] 2.1 新增租户在途计数器（Acquire/Release）与配置字段
- [ ] 2.2 接入各 lane 的候选循环前准入与全出口释放；指标与拒绝日志

## 3. 验收

- [ ] 3.1 `go test ./internal/proxy/ ./internal/config/ -race` 全绿
- [ ] 3.2 `make check` 全绿；`openspec validate tenant-concurrency-admission --strict` 通过
