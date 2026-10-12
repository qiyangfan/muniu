# Tasks: sighup-config-reload

- [x] 1.1 config.locate → Locate 导出;assemble 增 cfgPath 形参
- [x] 1.2 SIGHUP goroutine:重载→校验失败保持旧态/成功 ReplaceTopology 热替换
- [x] 1.3 cmd 集成测试 `TestSIGHUP_热加载与保持旧态`(SIGHUP 后新请求走新 provider/非法配置保持旧态,数据面 key 经同库签发)
- [x] 1.4 make check 全绿;archive + 主 specs 同步
