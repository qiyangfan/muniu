# Design

## 结构决策

**per-provider Transport,而非全局单 transport + 动态切换**:每渠道一个 `*http.Client`(共享 `net.Dialer` 与连接池参数,差异仅 `Proxy` 字段与 TLS 配置来源)。未声明代理的渠道**共享现有缺省 client**(零声明部署连 client 实例都不新增,行为逐字节等价);声明代理的渠道按需构造,首个使用时惰性创建。

**判定落在配置加载期,不在请求期**:加载时把每个渠道解析为「直连(env)/ 固定代理 URL」二值结论存入拓扑快照;请求期零解析零回退链查找。SIGHUP 热替换自然携带新结论。

## 出站面接线

| 出站面 | 现状 client | 改造 |
|---|---|---|
| 推理出站 | `proxy.client`(全局单例) | 按渠道查 client 表;直连渠道返回缺省 client |
| 健康探针 | health 包自建/复用出站 | 装配层按渠道注入(探针本来就要经渠道适配器出站,顺 client 参数传递) |
| 余额查询 | `balance.balanceHTTP` 全局变量 | Service 增 per-channel client 注入;全局变量退役为缺省值 |

控制面外联(metering ingest、auth 吊销、config 拓扑拉取)维持各自 client,零改动。

## 校验实现

`config.Load` 内对两字段做 `url.Parse` + scheme 白名单(http/https),错误攒齐进既有汇总报错通道;socks5 单独提示「支持形态 http/https」。声明代理的渠道**忽略环境变量**(显式声明即完整接管,不做「代理失败回落 env」的叠加语义——叠加语义不可测)。

## 测试策略

- httptest 起本地伪代理(接受 CONNECT,记录隧道目标 host 后转发),断言:推理出站、探针、余额查询三面均经代理;渠道级覆盖顶层;零声明+env 场景与现状等价(伪 env 注入测试进程内 `t.Setenv`)。
- 加载期校验表驱动:非法 scheme × N 渠道攒齐断言。
- 突变验证:破坏「渠道级 > 顶层」优先级或「零声明等价」,相关测试必须变红。

## Non-goals

- socks5 支持(x/net 依赖白名单外;国内本地代理普遍同时暴露 http 口,http 形态已可对接 clash/v2ray)。
- 代理认证(`proxy_url` 含 userinfo 暂不支持?——stdlib `Transport.Proxy` 原生支持 URL 内嵌 userinfo 的 Proxy-Authorization,免费获得,不单独做配置面)。
- 控制面外联的代理拓扑(那是 kongming 部署网络问题,不是渠道问题)。
