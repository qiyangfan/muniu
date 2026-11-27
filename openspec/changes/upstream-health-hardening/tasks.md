# Tasks

## 1. Lane 覆盖（测试先行）

- [ ] 1.1 先写失败测试：渠道 degraded 时 embeddings/images/multimodal 候选被跳过、上游零调用；half-open 期各 lane 成功/失败都回填渠道状态；responses 状态查询在 degraded 时 503；`go test ./internal/proxy/ -run TestHealthLane` 先红
- [ ] 1.2 抽共享健康门卫并接入全部候选循环；状态查询路径单独判收缩

## 2. 半开并发与参数（测试先行）

- [ ] 2.1 先写失败测试：half-open 并发 10 个请求仅放行 `half_open_max_inflight` 个，其余 503 且零上游；成功恢复/失败回降；名额全出口释放；`go test ./internal/health/ -run TestHalfOpenInflight` 先红
- [ ] 2.2 实现 Manager 半开名额与配置 `health.half_open_max_inflight`；接入 proxy 请求生命周期
- [ ] 2.3 拆分 `passive_window_sec`/`passive_min_samples`/`passive_rate_milli`，缺省 300/20/300；断言调 `switch_*` 不影响被动熔断

## 3. 验收

- [ ] 3.1 `go test ./internal/proxy/ ./internal/health/ ./internal/config/ -race` 全绿
- [ ] 3.2 `make check` 全绿；`openspec validate upstream-health-hardening --strict` 通过
