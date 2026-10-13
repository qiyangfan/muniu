# Tasks: key-disable-toggle

- [x] 1.1 失败测试先行:auth 单测(停用拒绝含缓存路径/恢复放行字段无损/吊销不复活/Invalidate 后即时生效)+ store 行回读断言
- [x] 1.2 迁移 0009(`disabled_at` 可空列)+ store/auth 接线(`GetAPIKeyByHash` 带 `disabled_at`;`KeyRecord.Disabled`/`KeyInfo` 判定)
- [x] 1.3 管理面 PATCH `disabled` + 列表回显 + openapi.yaml 同步
- [x] 1.4 CLI `keys disable/enable <prefix>` + usage 文案(收敛时长如实)
- [x] 1.5 控制台:状态徽章三态 + 启停操作(组件测试覆盖)
- [x] 1.6 `make check` 全绿;archive + 主 specs 同步
