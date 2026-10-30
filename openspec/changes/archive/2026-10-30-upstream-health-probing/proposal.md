# Proposal

## Why

网关对上游故障目前是纯被动感知:key 级有 EMA 健康度+冷却/禁用,但存在三个真空档——①静默渠道(数小时无流量)的死活无人知晓,用户请求充当第一个探针并承受首次失败;②渠道级无聚合健康态,全 key 劣化的渠道仍靠防饿死下限持续漏拍真实流量(key 级 EMA 下限 0.1 是刻意的,但渠道级没有「整路收缩」);③部分劣化(别名持续换道但请求仍成功)零告警——`all_candidates_failed` 只抓彻底失败。DeepSeek 等厂商有官方状态页(Flashduty/RSS),但供应商自述滞后、多厂商覆盖不齐、组件到渠道的映射需人工维护,只能当运维情报不能当路由控制信号——自建多厂商探活是唯一可靠路径。

## What Changes

- 新增**渠道级健康子系统**(新包 `internal/health`),三类信号源融合为一个状态机:
  - **免费控制面探针(缺省全开,渠道可退订)**:持 key 渠道默认按全局间隔(缺省 300 秒)请求上游免费控制面端点(适配器声明,如 `GET {base_url}/models`),零货币成本——个人项目即插即用;连续失败达阈值(缺省 2)判控制面降级;`providers[].health.free_probe: false` 退订。
  - **付费推理探针(声明制)**:渠道声明 `probe_model` 即启用最小推理请求(max_tokens 微值),证推理面健康;不声明零付费请求,企业按需开——声明即开关。
  - **被动统计(零成本,全局缺省开启)**:聚合真实流量的换道/失败信号(复用 failover 判定输出),驱动同一状态机。
  - 探针结果零触碰 key 池状态(EMA/冷却/禁用),429 判「存活但受限」不计成败;**恢复判据与降级原因同层对称**:控制面降级由免费探针翻案、推理面降级由付费探针或真实请求翻案、被动降级只由真实流量翻案。
- **熔断降级路由**:渠道进入降级态后候选循环整路跳过(多候选别名透明切换,用户无感);单候选渠道降级期快速失败(503 `provider_circuit_open` 信封,不再空走全超时链);降级态到期转半开,由探针(或下一真实请求)试探成功后扩回并发射恢复事件。
- **三条新告警**(经既有 alerting 管道):`provider_degraded`(warning)、`provider_recovered`(info)、别名切换率越线 `switch_rate_exceeded`(warning,窗口换道率超阈值单次发射,回落解除)。
- **管理可见性**:`GET /admin/v1/upstream-health`(各渠道健康相位/最近探针结果/切换率),OpenAPI 单源同步再生类型,前端 Providers 页透出健康态。
- 新增顶层 `health` 配置节(全局总开关/探针间隔/失败阈值/熔断时长/切换率窗口与阈值),校验攒齐报全,example 同步;ADR 记录「被动熔断缺省生效」这一路由行为变化。

## Capabilities

### New Capabilities

- `upstream-health`:渠道级上游健康观测(主动探针+被动统计)、熔断状态机(降级/半开/恢复)、切换率统计告警与管理可见性。

### Modified Capabilities

- `webhook-alerting`:「去重与事件覆盖」requirement 的事件族清单扩展上游健康三事件(由健康状态机判定触发时机并调用既有管道发射)。

## Impact

- `internal/health`(新包):状态机纯函数 + 探针循环 + 被动统计聚合;循环纪律照 balance Service(panic 按渠道隔离、优雅停止、启动即首轮)。
- `internal/proxy`:候选循环收缩跳过(先于取 key)、单候选快速失败信封、换道信号输出。
- `internal/config`:顶层 `health` 节 + `providers[].health`;`muniu.example.yaml` 同步。
- `internal/server` + `contracts/openapi.yaml`:新管理端点 + 类型再生(`pnpm generate:api`);web Providers 页健康态展示。
- `docs/design/proxy-and-streaming.md`(判定表相邻语义)、新设计文档 `docs/design/upstream-health.md`、新 ADR。
- 复用:alerting.Notifier、adapter 出站栈(探针即最小 chat 请求)、balance Service 循环纪律先例;零新增依赖。
