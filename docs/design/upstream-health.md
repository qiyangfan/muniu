# 上游健康观测与熔断降级(upstream-health)

> 行为契约的规范源是 `openspec/specs/upstream-health`(归档后);本文是设计叙事与运维口径摘要。归属裁决与行为变化记录见 ADR-0019。

## 能力一句话

渠道级上游健康观测:免费控制面探针(缺省全开)+ 付费推理探针(声明制)+ 被动统计(真实流量)融合为统一状态机,渠道劣化自动收缩出候选域(多候选透明切换、单候选快速失败),恢复自动扩回并通知。

## 信号源对照表

| 信号 | 成本 | 缺省 | 证明范围 | 触发线 |
|---|---|---|---|---|
| 免费控制面探针(`GET {base}/models` 族,anthropic `/v1/models`) | 零货币 | 开(渠道可退订、总闸可关) | 网络/TLS/鉴权/控制面服务 | 连续失败 ≥ `failure_threshold`(缺省 2) |
| 付费推理探针(`probe_model` 最小补全,max_tokens 微值) | ≈元级/天/渠道 | 关(声明制) | 推理面(免费端点 200 不证推理面) | 同上 |
| 被动统计(真实流量渠道失败率) | 零 | 开 | 推理面真实表现 | 窗口失败率越线(样本 ≥ 最低样本数) |

适配器能力:openai 兼容族(openai/doubao/qwen/deepseek/moonshot/glm)与 anthropic 具备两类探针端点;gemini 原生 v1 不支持探针(该族渠道跳过探针,被动统计照常)——登记限制。

## 状态机与纪律

- **相位**:`healthy → degraded(候选域收缩) → open 计时到期 → half_open(放行试探) → recovered`;降级记录原因三值:control_plane / inference / passive。
- **同层恢复**:控制面降级由免费探针连续成功(达失败阈值)翻案;推理面降级由付费探针单次成功翻案;被动降级只由真实流量试探翻案——探针成功不得解除被动降级(probe_model 与失败模型可能不同 lane,跨层解除是不诚实恢复)。半开期真实请求成功是全层证据,任意原因均可恢复。
- **降级路由**:候选循环整路跳过降级渠道(先于路径判定与取 key,不计错误类);多候选别名透明切换;单候选 503 `provider_circuit_open` 快速失败(零上游调用、零计量行,与预检闸 402 同族);降级跳过不计入别名换道率(它是网关的处置结果,不是上游信号)。
- **探针零触碰 key 池**:取值来自装配期快照,不调 Pick/Report;429 判「存活但受限」不计成败(防探针自身限频误降级活渠道)。
- **切换率越线告警**:按别名窗口(缺省 300s)统计换道率,样本 ≥ `switch_min_samples`(缺省 20)且比率 ≥ `switch_rate_milli`(缺省 300)发一次 `switch_rate_exceeded` warning,回落解除重新武装;样本不足诚实不判定。
- **循环纪律**:照 balance Service 先例——启动即首轮、间隔推进、单渠道 panic 隔离、优雅停止挂 cleanup;状态为进程内存态,重启重置并由首轮探针校准(不持久化)。
- **事件**:provider_degraded(warning)/ provider_recovered(info)/ switch_rate_exceeded(warning),复用 webhook-alerting 管道;通道挂了经投递自监控落库可见(alert_delivery_status)。

## 配置契约

```yaml
health:
  enabled: true             # 总开关;false 时路由行为与无此能力逐字节一致
  free_probe_enabled: true  # 免费探针总闸
  probe_interval_sec: 300   # 探针间隔
  probe_timeout_sec: 10     # 探针单次超时
  failure_threshold: 2      # 连续失败阈值
  open_seconds: 300         # 熔断时长(到期转半开)
  switch_window_sec: 300    # 换道率窗口
  switch_rate_milli: 300    # 换道率阈值(30%)
  switch_min_samples: 20    # 最低样本数
# 渠道级(挂 provider 下):
#     health:
#       free_probe: false    # 渠道级退订免费探针
#       probe_model: up-a    # 声明即启用付费推理探针(须持 key)
```

## 运维口径

- **热更边界**:`health` 节与渠道探针声明属非热更域,SIGHUP 不生效需重启(与 balance 同口径);运行期 key 取值随拓扑快照动态映射。
- **个人/企业分层**:免费探针缺省全开满足个人项目零成本;付费探针按渠道声明,只给需要推理面早发现的渠道。
- **管理可见性**:`GET /admin/v1/upstream-health` 各渠道相位/原因/探针结果与别名换道率;控制台 Providers 页透出健康态。
