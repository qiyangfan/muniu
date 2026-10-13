# platform-data-plane Specification

## Purpose
TBD - created by archiving change sighup-config-reload. Update Purpose after archive.

## Requirements

### Requirement: SIGHUP 配置热加载

进程必须 (MUST) 响应 `SIGHUP`:按启动时同一解析链重读配置文件;新配置校验失败必须 (MUST) 保持旧配置继续服务并输出结构化告警;校验通过必须 (MUST) 经既有拓扑热替换入口原子替换 providers/routes/能力目录与池(provider 配置未变时池运行态无损,与控制面拉取同一入口同一语义)。热更范围必须 (MUST) 如实登记:热替换仅覆盖上述拓扑域,其余装配一律不热更(auth/metrics/metering 刷盘参数/管理面/静态价目兜底/告警装配/日志与超时等;价目运营态走管理面,告警 webhook 端点变更重启生效)。

#### Scenario: 合法新配置热替换生效

- **WHEN** 修改配置文件的 provider 指向并发送 SIGHUP
- **THEN** 新请求经新 provider 出站,旧 provider 配置未变部分运行态无损

#### Scenario: 非法新配置保持旧态

- **WHEN** 配置文件被改为非法(如未知 adapter)后发送 SIGHUP
- **THEN** 进程保持旧配置继续正常服务,告警留痕,绝不进入半更新态
