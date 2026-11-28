# Tasks

## 1. 契约与测试（先确认）

- [x] 1.1 确认 429 `tenant_inflight_exceeded` 契约与默认 0 语义；更新 traffic-governance 文档与 example
- [x] 1.2 先写失败测试：租户 A 占满后 A 新请求 429、B 正常；槽位释放后 A 恢复；断连/预算中断释放；0=不限；`go test ./internal/proxy/ -run TestTenantInflight` 先红

## 2. 实现

- [x] 2.1 新增租户在途计数器（Acquire/Release）与配置字段
- [x] 2.2 接入各 lane 的候选循环前准入与全出口释放；指标与拒绝日志
- [x] 2.3 embeddings/images 补租户准入；装配期注册 `muniu_tenant_inflight` gauge，拒绝日志带 tenant/request_id
- [x] 2.4 managed 从 `entitlements.quotas.max_inflight` 消费 per-tenant 上限，快照应用后整体替换，缺席回落本地

## 3. 验收

- [x] 3.1 `go test ./internal/proxy/ ./internal/config/ -race` 全绿
- [x] 3.2 `make check` 全绿；`openspec validate tenant-concurrency-admission --strict` 通过
