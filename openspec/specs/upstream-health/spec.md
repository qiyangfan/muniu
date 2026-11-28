# upstream-health Specification

## Purpose

渠道级上游健康观测与熔断降级:免费控制面探针(缺省全开、渠道可退订)与付费推理探针(声明制)加被动统计(真实流量换道/失败信号)融合为统一健康状态机,渠道劣化时自动收缩候选域(多候选别名透明切换、单候选快速失败),恢复时自动扩回并通知——把上游故障的用户可见影响从「首个请求承受失败」压缩为「零感知或快速失败」,并让静默渠道的死活可被感知。

## Requirements

### Requirement: 免费控制面探针(缺省开启,渠道可退订)

全局 `health.free_probe_enabled`(缺省 true)开启时,持有 key 的渠道必须 (MUST) 默认获得免费控制面探针:按探针间隔(缺省 300 秒)经该渠道 key 池请求上游免费控制面端点(适配器声明的路径,如 OpenAI 兼容族 `GET {base_url}/models`、anthropic `GET /v1/models`),零货币成本;渠道级 `providers[].health.free_probe: false` 必须 (MUST) 退订该渠道探针;连续失败达到阈值(缺省 2)必须 (MUST) 判渠道降级(控制面阴性:网络/鉴权/服务不可用,高置信度);探针结果必须 (MUST) NOT 写入 key 池状态(EMA、冷却、禁用零触碰);免费探针凭证不出现在任何日志与告警载荷;免费探针成功仅证明控制面存活,必须 (MUST) NOT 单独解除由推理面信号(被动统计或付费探针)触发的降级。

#### Scenario: 静默渠道死亡被免费探针发现

- **WHEN** 某 deepseek 渠道数小时无真实流量,上游控制面故障(域名不可达/服务宕/密钥被吊销)
- **THEN** 免费探针在故障后周期内连续失败 2 次,渠道进入降级态并发射 `provider_degraded` 告警,早于任何真实用户请求,全程零货币成本

#### Scenario: 渠道级退订零行为

- **WHEN** 渠道声明 `providers[].health.free_probe: false`
- **THEN** 该渠道零探针行为,被动统计照常工作

#### Scenario: 探针不污染 key 池

- **WHEN** 免费探针请求被上游 429 限频或 5xx 拒绝
- **THEN** 该 key 的 EMA、冷却计数、禁用状态均不变,仅渠道健康判定消费该结果

### Requirement: 付费推理探针(声明制)

渠道声明 `providers[].health.probe_model` 必须 (MUST) 追加启用推理面探针:按探针间隔发送一次最小推理请求(max_tokens 微值),连续失败达阈值判渠道降级(推理面阴性);未声明 `probe_model` 的渠道必须 (MUST) 零推理探针(零付费请求);声明即开关,不另设全局付费总闸;推理探针同样零触碰 key 池状态、429 判「存活但受限」不计成败。

#### Scenario: 声明制付费开关

- **WHEN** 渠道未声明 `probe_model`
- **THEN** 该渠道零推理请求,全程零付费;声明后按间隔发送最小推理请求

#### Scenario: 推理面阴性优先级

- **WHEN** 渠道免费探针成功但推理探针连续失败(控制面活、推理面坏)
- **THEN** 渠道进入降级态,降级原因标记为推理面

### Requirement: 健康状态机与同层恢复

健康判定必须 (MUST) 融合三类信号:免费控制面探针、付费推理探针(声明渠道)、被动统计(真实流量的换道与失败聚合,全局缺省开启);状态机必须 (MUST) 为 `healthy → degraded → open 到期 → half-open(试探) → recovered(扩回)` 的相位机,降级必须 (MUST) 记录原因(控制面/推理面/被动),且恢复判据必须 (MUST) 与降级原因同层:控制面降级由免费探针连续成功恢复;推理面降级由付费探针成功或下一真实请求试探成功恢复;被动降级必须 (MUST) 只由真实流量恢复(半开期放行下一真实请求作试探,成功扩回、失败回降级续期),探针成功必须 (MUST) NOT 解除被动降级;状态为进程内存态,重启后重置并由首轮探针立即校准。

#### Scenario: 同层恢复——被动降级不被探针解除

- **WHEN** 渠道因真实流量失败聚合进入被动降级,此后免费/推理探针均成功
- **THEN** 渠道保持降级态;半开期下一真实请求试探成功才扩回

#### Scenario: 同层恢复——控制面降级由免费探针翻案

- **WHEN** 渠道因免费探针连续失败降级(静默渠道,无真实流量),上游恢复后免费探针连续成功
- **THEN** 渠道直接扩回候选域并发射 `provider_recovered`,无须等待真实流量

#### Scenario: 半开试探失败回降

- **WHEN** 半开期真实请求试探失败或付费探针继续失败
- **THEN** 渠道回到降级态并重新计时熔断时长

### Requirement: 降级态的路由反应

降级态渠道必须 (MUST) 在候选循环中被整路跳过(先于取 key,不计错误类),多候选别名透明切换到其余候选;单候选别名在降级期必须 (MUST) 快速失败(503 `provider_circuit_open` 信封,不得空走全超时链,零上游调用);全局 `health.enabled`(缺省 true)关闭时零探针、零收缩、零快速失败,路由行为与本能力引入前一致。

#### Scenario: 多候选别名透明收缩

- **WHEN** 别名候选 [deepseek, moonshot] 中 deepseek 进入降级态,请求到达
- **THEN** 候选循环直接从 moonshot 开始,用户无感;deepseek 不被计入任何失败类

#### Scenario: 单候选渠道快速失败

- **WHEN** 单候选渠道处于降级态,请求到达
- **THEN** 返回 503 `provider_circuit_open` 信封,不发起上游调用、不消耗超时链

#### Scenario: 全局关闭零行为

- **WHEN** 配置 `health.enabled: false`
- **THEN** 无探针、无收缩、无快速失败,数据面行为与本能力引入前逐字节一致

### Requirement: 切换率越线统计告警

网关必须 (MUST) 按别名统计窗口(全局可配,缺省 300 秒)内的换道率(换道请求数 ÷ 总请求数):窗口内总请求数达到最低样本数(缺省 20,小流量不判定防误报)且换道率达到阈值(缺省 300 毫单位即 30%)时,经既有告警管道发射一次 `switch_rate_exceeded` 事件(warning),载荷携带别名、窗口换道率与阈值;比率回落到阈值以下必须 (MUST) 解除并重新武装,已触发期不重复发射;样本不足必须 (MUST) 诚实不判定而非以小分母臆测;该告警只统计因上游故障/限频引发的换道,适配性豁免跳过不计入。

#### Scenario: 部分劣化被统计告警捕获

- **WHEN** 某别名窗口内 40% 请求因上游 5xx/429 换道到次优候选(请求均成功,无 `all_candidates_failed`)
- **THEN** 发射一次 `switch_rate_exceeded` warning,载荷含别名与换道率;持续越线不重复发射

#### Scenario: 小流量窗口不判定

- **WHEN** 某别名窗口内仅 3 个请求且 1 次换道(比率高但样本远低于最低样本数)
- **THEN** 不判定、不发射切换率告警

### Requirement: 管理 API 与可见性

管理面必须 (MUST) 提供 `GET /admin/v1/upstream-health`(纳入既有管理面鉴权与审计):返回全局开关态、各渠道健康相位(healthy/degraded/half-open)与降级原因(控制面/推理面/被动)、探针配置与最近探针结果(时刻/成败/错误归类)、降级与恢复时刻、各别名当前窗口换道率;契约必须 (MUST) 同步进 OpenAPI 单源并再生前端类型;前端必须 (MUST) 在 Providers 页透出各渠道健康态(含降级态显著标识),错误走既有 ErrorAlert/NoticeBanner 纪律。

#### Scenario: 总览包含相位与探针结果

- **WHEN** 某 deepseek 渠道处于降级态且声明推理探针
- **THEN** 端点返回该渠道相位 degraded、降级原因、降级时刻、最近两次探针成败与错误归类;Providers 页该渠道显著标识降级

### Requirement: 配置契约

顶层 `health` 节必须 (MUST) 提供:总开关(缺省开启)、免费探针总开关(缺省开启)、探针间隔秒(缺省 300)、探针超时秒(缺省 10)、连续失败阈值(缺省 2)、熔断时长秒(缺省 300)、切换率窗口秒(缺省 300)、切换率阈值毫单位(缺省 300)、切换率最低样本数(缺省 20);`providers[].health` 必须 (MUST) 提供 `free_probe`(渠道级退订开关,缺省跟随全局)与 `probe_model`(字符串,声明制付费推理探针);全部数值必须 (MUST) 为正整数(开关布尔除外),非法值必须 (MUST) 校验期拒绝并攒齐一次报全;`probe_model` 声明的渠道必须 (MUST) 在 key 池至少持有一把 key(无 key 渠道探针无从发起,校验期拒绝);example 配置样例必须 (MUST) 同步。

#### Scenario: 非法探针间隔拒绝

- **WHEN** 配置 `health.probe_interval_sec: 0`
- **THEN** 启动校验失败,错误指明字段与合法域

#### Scenario: 无 key 渠道声明推理探针拒绝

- **WHEN** 渠道声明 `probe_model` 但 `keys` 为空
- **THEN** 启动校验失败,错误指明渠道名

#### Scenario: 校验错误一次报全

- **WHEN** 两个渠道分别存在非法间隔与无 key 声明推理探针
- **THEN** 校验错误同时包含两处问题

### Requirement: Gemini 原生免费控制面探针

Gemini 原生适配器渠道必须 (MUST) 默认获得免费控制面探针:按探针间隔请求 `GET {base_url}/models`(Google models.list),以 `x-goog-api-key` 鉴权,零货币成本;全局 `health.free_probe_enabled` 与渠道级 `providers[].health.free_probe: false` 的退订语义与其他适配器一致;探针结果零触碰 key 池状态(EMA/冷却/禁用)。

#### Scenario: Gemini 静默渠道控制面死亡被发现

- **WHEN** 某 gemini 渠道数小时无真实流量,上游控制面故障(域名不可达/服务宕/密钥被吊销)
- **THEN** 免费探针按间隔发起 `GET {base_url}/models`,连续失败达阈值后渠道进入降级态并发射 `provider_degraded` 告警,早于任何真实用户请求,全程零货币成本

#### Scenario: Gemini 探针鉴权形态

- **WHEN** gemini 渠道的免费探针出站
- **THEN** 请求携带 `x-goog-api-key` 头,不携带其他适配器族的鉴权头

#### Scenario: Gemini 渠道退订零探针

- **WHEN** gemini 渠道声明 `providers[].health.free_probe: false`
- **THEN** 该渠道零探针请求,被动统计照常工作

### Requirement: Gemini 推理探针声明拒绝

Gemini 渠道声明 `providers[].health.probe_model` 必须 (MUST) 在配置校验期拒绝并明确报错,不得静默忽略或降级为免费探针;推理探针启用范围仅限已声明推理端点形态的适配器。

#### Scenario: Gemini 声明 probe_model 被拒绝

- **WHEN** 某 gemini 渠道持有 key 并声明 `health.probe_model: gemini-pro`
- **THEN** 配置加载失败,错误信息指明该渠道与 `probe_model` 不受支持

### Requirement: 探针取样避开不可用 key

探针必须 (MUST) 从池内选择非禁用且不在冷却期的 key 作为取样目标,并按渠道轮换;不得 (MUST NOT) 调用 `Pick/Report` 或写入 EMA、冷却、禁用状态。池内无可用 key 时探针必须 (MUST) 跳过本轮,不得 (MUST NOT) 计入失败或推进降级。

#### Scenario: 首键撤销不再熔断渠道

- **WHEN** 渠道首把 key 因 401 被禁用,其余 key 健康
- **THEN** 免费探针使用其余健康 key,渠道相位保持 healthy

#### Scenario: 首键冷却不污染推理探针

- **WHEN** 渠道首把 key 处于欠费冷却,其余 key 可用
- **THEN** 推理探针不选择该冷却 key,也不因它的 402 判渠道降级

#### Scenario: 全部不可用跳过探针

- **WHEN** 渠道全部 key 均禁用或冷却
- **THEN** 本轮不发起探针请求,不增加连续失败计数;数据面按池耗尽语义处理

#### Scenario: 轮换覆盖多 key

- **WHEN** 渠道有两把健康 key 且连续多轮探针
- **THEN** 探针取样在 key 间轮换,不固定首键

### Requirement: 被动统计只消费渠道级证据

渠道被动失败窗口与半开试探裁决必须 (MUST) 只消费渠道级证据:429/401/403/欠费不得计入;dial/TLS/首 token 超时/5xx/首 token 前断流计入;400/404 与客户端断连、预算中断不计入。证据分类以 `error-evidence-scope` 判定表为唯一源。

#### Scenario: 429 风暴不降渠道

- **WHEN** 单渠道多把 key 轮流返回 429,期间持续有请求成功
- **THEN** 渠道被动窗口不计入这些 429,渠道相位保持 healthy

#### Scenario: 5xx 触发被动降级

- **WHEN** 渠道真实流量窗口内 5xx 失败率达到阈值且样本充足
- **THEN** 渠道进入被动降级,与既有阈值语义一致

#### Scenario: 参数错误不计失败

- **WHEN** 渠道返回 400 或 404
- **THEN** 被动窗口 total 与 failure 均不增加

### Requirement: 半开试探只由渠道级成败裁决

半开期真实请求必须 (MUST) 仅在渠道级成功或渠道级失败时裁决恢复/回降;key 级限频、鉴权失败与请求级参数错误不得 (MUST NOT) 使渠道回降级。

#### Scenario: 半开期 key 限频不误判

- **WHEN** 渠道处于 half-open,真实请求因单 key 429 换道后成功
- **THEN** 渠道按成功试探恢复,不因 429 回降级

### Requirement: 熔断覆盖全部数据面 lane

所有实际发起上游候选尝试的数据面路径(chat/messages/responses/gemini、embeddings、images、multimodal)必须 (MUST) 在候选级查询渠道熔断态并跳过降级渠道,且把渠道级成败回填被动窗口与半开试探;responses 状态查询路径必须 (MUST) 在熔断期快速失败 503 `provider_circuit_open`(该路径无法换道)。跳过的候选不得 (MUST NOT) 计入错误类或换道率。

#### Scenario: 非 chat lane 跳过熔断渠道

- **WHEN** 某渠道在 chat lane 已进入 degraded,embeddings/images/multimodal 请求的别名包含该渠道
- **THEN** 这些 lane 的候选循环跳过该渠道,上游零调用,用户由其余候选服务

#### Scenario: 半开试探跨 lane 生效

- **WHEN** 渠道处于 half-open,首个真实请求来自 embeddings lane 并成功
- **THEN** 渠道按成功试探恢复,chat lane 随后恢复使用

#### Scenario: 状态查询快速失败

- **WHEN** responses 状态查询的目标渠道处于 degraded
- **THEN** 返回 503 `provider_circuit_open`,不发起上游查询

### Requirement: 半开期并发试探上限

半开期必须 (MUST) 限制同时在途的真实试探请求数为 `health.half_open_max_inflight`(缺省 1,0=不限);超额请求必须 (MUST) 快速失败 503 `provider_circuit_open` 且不触上游;试探成功或失败必须 (MUST) 释放名额并裁决恢复或回降级。

#### Scenario: 半开并发只放一个试探

- **WHEN** 渠道 half-open,同时到达 10 个请求,上限为 1
- **THEN** 仅 1 个请求打到上游,其余 9 个 503;上游零调用

#### Scenario: 试探成功恢复

- **WHEN** 该唯一试探请求成功
- **THEN** 渠道恢复 healthy,后续请求正常放行

#### Scenario: 名额释放

- **WHEN** 试探请求因断连或预算中断终结
- **THEN** 名额释放,后续半开请求可再次试探

### Requirement: 被动统计独立参数

渠道被动失败窗口必须 (MUST) 使用独立的 `health.passive_window_sec`、`passive_min_samples`、`passive_rate_milli`(缺省 300/20/300),不得 (MUST NOT) 与换道率告警的 `switch_*` 参数共用;修改告警阈值不得改变被动熔断判定。

#### Scenario: 告警参数不影响熔断

- **WHEN** 将 `switch_rate_milli` 调整为 100,`passive_rate_milli` 保持 300
- **THEN** 换道率告警按 10% 触发,被动熔断仍按 30% 判定

#### Scenario: 缺省等值

- **WHEN** 配置未声明任何 `passive_*` 字段
- **THEN** 被动窗口按 300 秒、20 样本、30% 运行,与变更前一致
