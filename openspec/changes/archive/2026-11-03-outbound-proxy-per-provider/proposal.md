# Proposal

## Why

出站代理当前只有进程级环境变量口(`internal/proxy` 的 transport 挂 `http.ProxyFromEnvironment`):单进程同服国内外上游时无法分流——让国际渠道走代理则国内渠道也被拖进去,靠 `NO_PROXY` 枚举域名兜底是运维脆弱面(新增渠道忘配即断)。国际端点渠道(预设目录中六条国际家)在国内部署没有代理出口就不可达,渠道级出站代理声明缺失。

背景:2026-11-03 Magpie 竞品二次裁决立项之一(裁决与核验证据见 HANDOFF 验收记录 56)。

## What Changes

- 新增 `providers[].proxy_url`(渠道级出站代理声明,空=未声明)与顶层 `outbound_proxy`(全局缺省回退)。生效优先级:**渠道级 > 顶层缺省 > 环境变量**;两级均未声明时保持 `ProxyFromEnvironment` 既有行为——零声明部署与现状逐字节等价。
- 代理形态仅 `http://` / `https://`(stdlib `http.Transport.Proxy` CONNECT 隧道,零新依赖);`socks5://` 等其他 scheme 在加载期拒绝并提示支持形态。
- 渠道级代理作用于该渠道**全部上游出站面**:推理出站、健康探针出站、余额查询出站;控制面外联(计量汇 ingest、吊销订阅、拓扑拉取)不消费渠道级代理。
- 加载期校验攒齐报全;代理不可达属运行态失败,走既有出站错误分类(拨号失败→换道),不进配置校验。

## Capabilities

### New Capabilities

- `outbound-proxy`:渠道级出站代理声明、回退优先级、出站面覆盖范围与加载期校验。

### Modified Capabilities

(无)

## Impact

- `internal/config`:`Provider` 增 `ProxyURL` 字段、顶层 `OutboundProxy`,加载期校验。
- `internal/proxy`:per-provider `http.Client`/`Transport` 装配(超时与连接池参数维持现状),出站按渠道取用;未声明渠道共享缺省 client(即现状 client)。
- `internal/health`、`internal/balance`:探针与余额查询出站按渠道消费代理(装配层注入 per-channel client)。
- `muniu.example.yaml` 样例;`docs/design/proxy-and-streaming.md` 出站节补记。
- 零新增依赖;零管理面/OpenAPI 变化(纯配置面)。
