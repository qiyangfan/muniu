# outbound-proxy Specification

## Purpose
渠道级出站代理声明:解析优先级渠道 > 顶层 > 环境变量,零声明部署行为与无此能力逐字节一致;代理覆盖该渠道推理出站、健康探针与余额查询三面(控制面外联不消费),声明即完整接管环境变量。形态仅 http/https,非法声明加载期攒齐拒绝,代理不可达走既有换道语义不进配置校验。

## Requirements

### Requirement: 渠道级出站代理声明与回退优先级

网关必须 (MUST) 支持渠道级出站代理声明 `providers[].proxy_url` 与顶层缺省 `outbound_proxy`,生效优先级为渠道级 > 顶层缺省 > 环境变量(`HTTP_PROXY`/`HTTPS_PROXY` 既有行为);两级均未声明时出站行为必须 (MUST) 与现状逐字节一致(`ProxyFromEnvironment`)。代理形态必须 (MUST) 仅为 `http://` 或 `https://`。

#### Scenario: 渠道级覆盖顶层缺省

- **WHEN** 顶层 `outbound_proxy` 已声明且某渠道另行声明 `proxy_url`
- **THEN** 该渠道全部出站走渠道级代理,其余未声明渠道走顶层缺省

#### Scenario: 零声明等价现状

- **WHEN** 配置未声明任何 proxy 字段且环境变量设置了 `HTTP_PROXY`
- **THEN** 出站走环境变量代理(与现状一致,无行为变化)

#### Scenario: 渠道声明对环境变量免疫

- **WHEN** 渠道声明 `proxy_url` 且环境变量亦设置了代理
- **THEN** 该渠道走声明代理,环境变量对该渠道失效

### Requirement: 渠道级代理覆盖渠道全部上游出站面

渠道声明的代理必须 (MUST) 作用于该渠道的推理出站、健康探针出站与余额查询出站;控制面外联(计量汇 ingest、吊销订阅、拓扑拉取)必须 (MUST) 不消费渠道级代理。

#### Scenario: 探针经代理出站

- **WHEN** 渠道声明代理且免费探针开启
- **THEN** 探针请求经该代理出站,代理不可达计入探针失败计数(与推理出站失败同口径)

#### Scenario: 余额查询经代理出站

- **WHEN** 渠道声明代理且配置了余额源
- **THEN** 余额查询经该代理出站

#### Scenario: 控制面外联不受渠道代理影响

- **WHEN** 渠道声明代理且计量汇为 ingest 形态
- **THEN** ingest 出站不经渠道级代理(控制面网络位置独立于上游渠道)

### Requirement: 非法代理声明加载期拒绝

`proxy_url`/`outbound_proxy` 必须 (MUST) 在配置加载期校验:scheme 不属 http/https(含 socks5)必须 (MUST) 报错拒绝且多渠道错误攒齐报全;代理运行态不可达必须 (MUST) 不进配置校验,按既有出站错误分类处理(拨号失败→换道)。

#### Scenario: socks5 声明被拒

- **WHEN** 配置声明 `proxy_url: socks5://127.0.0.1:1080`
- **THEN** 加载期报错拒绝,提示支持形态为 http/https

#### Scenario: 多渠道非法声明攒齐报全

- **WHEN** 两个渠道分别声明非法 scheme 的 `proxy_url`
- **THEN** 加载期一次性报出两处错误,不逐个暴露

#### Scenario: 代理不可达走既有换道语义

- **WHEN** 渠道代理进程运行中不可达
- **THEN** 该渠道请求按拨号失败进入既有错误分类与候选换道;不触发配置报错
