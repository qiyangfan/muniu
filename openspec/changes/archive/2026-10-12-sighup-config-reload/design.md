# Design: sighup-config-reload

- `config.locate` 导出为 `Locate`(main 复用同一解析链,消路径双写)。
- `assemble` 增 `cfgPath` 形参;SIGHUP goroutine 与控制面拉取共用 `ReplaceTopology`+`buildPool` 闭包——同一入口同一语义,不新建第二条替换路径。
- 热更范围(auth/metrics/管理面/静态价目兜底不热更)写进配置注释与 HANDOFF,防「SIGHUP 了怎么没生效」的运维误判。
