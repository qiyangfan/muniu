# ADR-0009:/metrics 实现——Prometheus 文本协议零依赖直写

- 状态:已接受(2026-09-11)

## 决策

`/metrics` 端点以 **Prometheus 文本暴露格式直写**(stdlib 实现 counter/gauge/histogram 与标签,约百行),不引入 `prometheus/client_golang`。端点默认关闭;启用时挂载于管理面(token 鉴权,Prometheus scrape 支持 Authorization 配置),与业务链路隔离;采集全部为原子计数/直方图操作,不同步阻塞请求路径。

## 理由

- 依赖白名单制纪律:client_golang 拖入 prometheus/common 等依赖树,而本仓指标集完全自控,文本格式(v0.0.4,稳定)手写成本约百行——为格式化器引入一棵依赖树不划算;
- 零依赖与「单二进制零外联」品格同源(ADR-0001)。

## 备选与否决

- `prometheus/client_golang`:生态标准但依赖树重;若未来需要 provider 生态/push gateway 再按 ADR 增补。

## 后果

histogram 直方图桶与文本转义需自行维护(附契约测试钉住输出格式);OpenMetrics 特性不支持(v1 不需要)。
