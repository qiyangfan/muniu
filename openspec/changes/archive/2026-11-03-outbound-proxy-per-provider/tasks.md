# Tasks

## 1. 配置面(测试先行)

- [x] 1.1 失败测试先行:`proxy_url`/`outbound_proxy` 校验——非法 scheme 报错、socks5 拒绝并提示 http/https、多渠道攒齐报全、合法 http/https 通过(先红后绿)
- [x] 1.2 `internal/config`:`Provider.ProxyURL` + 顶层 `OutboundProxy` 字段与加载期解析(渠道 > 顶层 > env 的结论预计算入拓扑快照)
- [x] 1.3 `muniu.example.yaml` 样例与注释(声明=完整接管 env 的语义写进注释)

## 2. 推理出站(测试先行)

- [x] 2.1 失败测试先行:渠道级覆盖顶层、零声明等价现状(伪 env)、渠道声明免疫 env——htptest 伪代理 CONNECT 记录隧道目标(先红后绿)
- [x] 2.2 `internal/proxy`:per-provider client 装配(惰性构造、直连渠道复用缺省 client),出站按渠道取用
- [x] 2.3 SIGHUP 热替换后 client 表随拓扑重建(旧 client 不泄漏:关闭 idle 连接)

## 3. 探针与余额出站

- [x] 3.1 health 探针按渠道消费代理(装配注入),失败计数与推理面同口径;回归既有探针测试
- [x] 3.2 balance Service per-channel client 注入(`balanceHTTP` 全局变量退役为缺省);回归既有余额测试

## 4. 文档与收尾

- [x] 4.1 `docs/design/proxy-and-streaming.md` 出站节补记(优先级/覆盖面/校验语义)
- [x] 4.2 突变验证:破坏回退优先级与「零声明等价」各一处,确认测试变红后还原复绿
- [x] 4.3 `make check` 全绿(vet + lint + race + coverage gate);`openspec validate --all --strict` 通过
