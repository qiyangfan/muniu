## Purpose

Prometheus 指标端点:集群形态的运维基础(多副本监控/告警/容量评估),文本协议暴露转发与内部管道健康。

## ADDED Requirements

### Requirement: 指标端点

网关必须 (MUST) 提供 `/metrics` 端点(Prometheus 文本协议),至少覆盖:请求计数与延迟直方图(按模型别名/状态)、上游调用结果分类计数(failover 判定类别)、计量管道水位(channel 占用/批量落库延迟/缓冲队列长度)、key 池状态计数(冷却/禁用/EMA 分布)。

#### Scenario: 指标可抓取
- **WHEN** 以 Prometheus 文本协议抓取 `/metrics`
- **THEN** 返回合法指标集,包含上述四类指标且标签维度齐全

### Requirement: 端点隔离与低开销

`/metrics` 必须 (MUST) 与业务链路隔离(不经验权/配额链,默认仅绑定管理地址或由配置显式开启);指标采集必须 (MUST) 有界开销(计数器/直方图原子操作,不得为采集同步阻塞请求路径)。

#### Scenario: 未配置不暴露
- **WHEN** 配置未启用 metrics 端点
- **THEN** `/metrics` 不注册(404),零额外开销
