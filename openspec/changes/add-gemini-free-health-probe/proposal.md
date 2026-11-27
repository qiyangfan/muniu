# Proposal: add-gemini-free-health-probe（Gemini 免费控制面探针）

> 优先级：P0（错误登记更正 + 低风险补洞）。关联 ADR：`docs/adr/0019-upstream-health-circuit-breaking.md`（2026-11-27 更正）。

## Why

`docs/design/upstream-health.md` 与 ADR-0019 把「gemini 原生不支持探针」登记为限制，但该结论与事实不符：仓内样例 `base_url` 为 `v1beta`，Google API 参考提供 `GET /v1beta/models`（models.list），适配器鉴权为 `x-goog-api-key`。结果是 Gemini 原生渠道在无真实流量时没有任何控制面健康信号，静默渠道故障只能由首个用户请求承受。推理探针仍不实现；但免费控制面探针可以零货币成本补上。

## What Changes

- `internal/health`：探测器适配器表新增 gemini 免费探针（`GET {base_url}/models`），新增 gemini 鉴权形态（`x-goog-api-key`）。
- 将探针鉴权从 `anthropic bool` 收敛为显式鉴权枚举（Bearer / Anthropic 三态 / Gemini API key），避免继续叠加布尔分支。
- `cmd/muniu` 装配：Gemini 渠道默认获得免费探针（仍受全局总闸与渠道级 `free_probe: false` 约束）；`probe_model` 在 Gemini 上不生效且不得静默忽略。
- `internal/config`：Gemini 渠道声明 `probe_model` 必须校验期拒绝，提示推理探针暂不支持。
- **文档更正**：`docs/design/upstream-health.md`、ADR-0019、`muniu.example.yaml`、`internal/health/probe.go` 注释中「gemini 不支持探针」改为「免费探针支持；推理探针暂不实现」。
- 推理探针（`probe_model`）对 Gemini 不在本变更范围。

## Capabilities

### New Capabilities

无

### Modified Capabilities

- `upstream-health`：新增 Gemini 原生免费控制面探针的适配器覆盖与 `probe_model` 声明拒绝语义。

## Impact

- 代码：`internal/health/probe.go`、`internal/health/manager.go`、`cmd/muniu/main.go`、`internal/config/config.go` 及对应测试。
- 文档：`docs/design/upstream-health.md`、`docs/adr/0019-upstream-health-circuit-breaking.md`、`muniu.example.yaml`。
- 行为：Gemini 原生渠道会新增每探针周期一次 `GET /models` 外呼（零货币成本，可退订）；数据面转发、key 池状态与判定表零改动。
- 依赖：零新增依赖。
