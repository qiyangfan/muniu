# Tasks

## 1. 契约与测试（先 ADR）

- [ ] 1.1 补 ADR：首 token 前预算默认值、504 错误码、池耗尽 429/503 分级；更新 failover 判定表与错误信封文档
- [ ] 1.2 先写失败测试：N 个候选挂起在 deadline 命中 504 且停止换道；首 token 后长流不被截断；max_attempts 边界；0=不限
- [ ] 1.3 先写失败测试：Retry-After 秒/HTTP-date/非法/过去时刻四形态；池全冷却 429 + Retry-After；池禁用 503；混合原因维持既有信封

## 2. 实现

- [ ] 2.1 候选循环接入 deadline 与 max_attempts（跳过不计），新增 504 信封
- [ ] 2.2 keypool 只读 `Availability(now)` 与池耗尽分级信封
- [ ] 2.3 `parseRetryAfter` 支持 HTTP-date

## 3. 验收

- [ ] 3.1 `go test ./internal/proxy/ ./internal/keypool/ -race` 全绿
- [ ] 3.2 `make check` 全绿；`openspec validate failover-hardening --strict` 通过
