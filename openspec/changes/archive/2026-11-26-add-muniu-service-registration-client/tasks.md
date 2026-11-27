# Tasks

## 1. 注册客户端

- [x] 1.1 服务身份注册/心跳/能力/版本/幂等。验证：注册可见、last_seen 更新、重复上报不重复实例。（client 稳定 instance_id + 幂等 POST；fast 测试 TestRegistrar_退避与恢复补报）
- [x] 1.2 managed 启用/standalone 禁用。验证：managed 注册；standalone 不报错、不阻塞。（standalone 仅本地命令执行器，不启动网络循环）
- [x] 1.3 控制面缺席退避/恢复补报。验证：启动/数据面不阻塞；锦官恢复后注册成功。（指数退避 1s→60s；TestRegistrar_退避与恢复补报）
- [x] 1.4 认证与脱敏负向。验证：匿名 401/403；payload 无凭据/连接串/服务域配置。（客户端恒带服务身份、payload 字段白名单测试；接收面匿名拒绝归 X-01 联调）

## 2. 契约

- [ ] 2.1 与锦官 service-registry envelope/能力键契约测试。验证：跨仓契约测试必跑（X-01）。**阻塞：锦官 X-01 schema 未冻结、接收面未实施。**
- [ ] 2.2 能力/版本单一来源。验证：发布后心跳可见；契约漂移即红。本地单一来源已实现（Capabilities/EntitlementKeys/AdminAPIs）；**跨仓漂移门禁阻塞：X-01 未冻结。**

## 3. 验收与独立运行

- [x] 3.1 权限负向与跨租户用例。验证：未授权 401/403，跨租户 404/不可见。（服务只读 API 匿名 401；新增运行时路由 GLOBAL-only 分类测试）
- [x] 3.2 审计断言。验证：注册/心跳结果可审计，无敏感值。（状态迁移写 system 审计；逐心跳只留结构化日志；e2e 断言）
- [x] 3.3 独立运行/降级 smoke。验证：停锦官启动/数据面不受影响；恢复后补报。（TestManaged_无快照failClosed / TestManaged_缺席时用lastKnown起服 / 退避恢复测试）
- [ ] 3.4 契约与生成客户端。验证：OpenAPI/生成物无漂移；跨仓契约测试必跑（X-01）。**阻塞：X-01 未冻结。**
- [x] 3.5 Greenfield 重置：删除/禁用旧注册路径。验证：无兼容/双轨，旧路径不存在。（仓内无旧注册路径；旧控制拉取键加载期拒绝）

## Workflow follow-up

- 归档前跑 `openspec validate --strict`；实现完成后按仓库门禁收口。
- 本 change 与 longzhong 聚合层 `docs/CONTROL-PLANE-PROJECT.md` 及 `docs/CONTROL-PLANE-BOUNDARY.md` 的对应 WP/边界表保持一致。
