# Tasks

## 1. 快照消费

- [x] 1.1 统一拉取/缓存/版本收敛。验证：304/版本单调。（TestSnapshot_验签应用版本单调与lastKnown）
- [x] 1.2 价目/拓扑/吊销/配额/共享策略包应用。验证：原子替换/回退保留旧。（先构建后单事务落库再切换；策略包只投影）
- [x] 1.3 服务身份 + 签名校验。验证：匿名 401/403；签名失败拒绝。（RSA-SHA256 验签；无效签名用例）
- [x] 1.4 managed 关闭本地价目/拓扑/租户写入。验证：只读/拒绝；standalone 可写。（价目/租户写 403、SIGHUP 忽略、控制台只读；standalone 既有测试）

## 2. 失败语义

- [x] 2.1 安全类 fail-closed。验证：缺失/非法拒绝。（缺吊销/租户整包拒绝；无 last-known 时 503）
- [x] 2.2 standalone last-known。验证：锦官缺席启动。（TestManaged_缺席时用lastKnown起服；stale 标记）

## 3. 验收与独立运行

- [x] 3.1 权限负向与跨租户用例。验证：未授权 401/403，跨租户 404/不可见。（admin scope 既有测试 + 服务只读 API 401）
- [x] 3.2 审计断言。验证：每次写操作有审计记录；普通列表只读不写审计；敏感详情/导出按审计策略。（快照应用 system 审计；admin 中间件既有非 GET 审计）
- [x] 3.3 独立运行/降级 smoke。验证：停掉管理面或其他仓，本服务核心流程不受影响；管理面停服仅局部不可用。（last-known/无快照 fail-closed 两组 e2e）
- [ ] 3.4 契约与生成客户端。验证：OpenAPI/生成物无漂移；跨仓契约测试必跑（X-01）。**阻塞：X-01 未冻结；本仓 OpenAPI 与生成类型已同步。**
- [x] 3.5 Greenfield 重置。验证：无兼容/双轨分支。（旧 pricing/topology/revocations/ingest 配置键加载期拒绝；managed 本地共享控制写入口关闭）

## Workflow follow-up

- 归档前跑 `openspec validate --strict`；实现完成后按仓库门禁收口。
- 本 change 与 longzhong 聚合层 `docs/CONTROL-PLANE-PROJECT.md` 及 `docs/CONTROL-PLANE-BOUNDARY.md` 的对应 WP/边界表保持一致。
