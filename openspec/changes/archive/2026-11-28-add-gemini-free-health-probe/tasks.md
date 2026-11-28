# Tasks

## 1. 探针能力（测试先行）

- [x] 1.1 先写失败测试：`ProbeSupported("gemini")` 为真、`InferenceProbeSupported("gemini")` 为假；gemini 渠道免费探针请求 `GET /models` 且携带 `x-goog-api-key`、不带 `Authorization`；`go test ./internal/health/ -run TestManager_Gemini` 先红
- [x] 1.2 实现 gemini probeSpec 与鉴权枚举，收敛 `anthropic bool`；`go test ./internal/health/ -race` 全绿

## 2. 推理探针声明拒绝（测试先行）

- [x] 2.1 先写失败测试：gemini 渠道声明 `probe_model` 时配置加载失败且错误含渠道名与字段；`go test ./internal/config/ -run TestHealth_缺省与校验` 先红
- [x] 2.2 配置校验接入 `InferenceProbeSupported`；装配层 `probe_model` 设置与 tick 同步改用该判定；`go test ./internal/config/ ./cmd/muniu/ -race` 全绿

## 3. 文档更正

- [x] 3.1 `docs/design/upstream-health.md`、ADR-0019、`muniu.example.yaml`、`internal/health/probe.go` 注释中「gemini 不支持探针」改为「免费探针支持；推理探针暂不实现」

## 4. 验收

- [x] 4.1 `go test ./internal/health/ ./internal/config/ ./cmd/muniu/ -race` 全绿
- [x] 4.2 `make check` 全绿；`openspec validate add-gemini-free-health-probe --strict` 通过
