## MODIFIED Requirements

### Requirement: 配置源双驱动

providers、model_routes、pricing 三类拓扑必须 (MUST) 支持「本地文件(现状)」与「控制面版本化拉取」两种配置源;两源均必须 (MUST) 经同一套校验后原子热替换运行态目录(在途请求用旧快照完成,新请求即用新快照);版本化拉取必须 (MUST) 支持条件请求(未变更不传输)与变更校验和;控制面不可达时必须 (MUST) 保持最后一次成功快照继续服务(降级可用),启动时无任何快照且控制面不可达必须 (MUST) 启动失败(fail-fast)。provider 的可选 `native_multimodal_embeddings_url` 必须 (MUST) 同时存在于 YAML 与 topology JSON provider 形态中，经相同 URL 校验并参与 provider 变更判定；字段变更必须 (MUST) 重建对应 adapter，未变更 provider 必须 (MUST) 保持池状态。

#### Scenario: 控制面热替换原生 endpoint

- **WHEN** 控制面 topology 更新 provider 的 `native_multimodal_embeddings_url`
- **THEN** topology 校验通过后新请求使用新 endpoint，旧请求以旧快照完成；池状态按既有 provider 变更语义处理

#### Scenario: 不合法 endpoint 保持旧快照

- **WHEN** topology 下发的 URL 非绝对 HTTP(S) URL 或包含非法结构
- **THEN** 新快照整体拒绝，运行中的旧拓扑与 adapter 保持不变并告警

#### Scenario: 热更新无重启

- **WHEN** 控制面变更渠道拓扑并递增版本,网关完成一轮拉取
- **THEN** 新请求按新拓扑路由计价,服务全程无重启、在途请求不受影响

#### Scenario: 控制面失联降级

- **WHEN** 控制面持续不可达
- **THEN** 网关以最后一次成功快照继续转发与计量,并按固定间隔告警日志暴露降级状态

#### Scenario: 快照损坏拒绝应用

- **WHEN** 拉取的快照未通过校验(校验和不符或结构非法)
- **THEN** 丢弃该快照、保持旧快照、告警,绝不放行脏配置
