# Tasks

## 1. 上报

- [x] 1.1 usage/health envelope 与幂等。验证：双发/补发。（source=muniu envelope；row ID 幂等；心跳健康独立）
- [x] 1.2 缓冲上限/失败告警。验证：不阻断热路径。（复用 metering_buffer + OnSinkDown 告警；TestUsageSink_本地落库与source）
- [x] 1.3 服务身份认证。验证：匿名 401/403。（上报恒带服务身份；接收面匿名拒绝归 X-01）
- [ ] 1.4 LLM 单一来源。验证：其他服务 LLM 事件拒绝/丢弃。本仓固定 source=muniu；**接收面拒绝其他 LLM 来源阻塞：X-01/锦官 ingest 未冻结。**

## 2. 验收与独立运行

- [x] 2.1 权限负向与跨租户用例。验证：未授权 401/403，跨租户 404/不可见。（上报为出站；服务只读 API 401 已证）
- [x] 2.2 审计断言。验证：每次写操作有审计记录；普通列表只读不写审计；敏感详情/导出按审计策略。（出站用量不逐事件写审计；控制面接收审计归锦官）
- [x] 2.3 独立运行/降级 smoke。验证：停掉管理面或其他仓，本服务核心流程不受影响；管理面停服仅局部不可用。（失败落缓冲/本地计量保留测试）
- [ ] 2.4 契约与生成客户端。验证：OpenAPI/生成物无漂移；跨仓契约测试必跑（X-01）。**阻塞：X-01 未冻结。**
- [x] 2.5 Greenfield 重置。验证：无兼容/双轨分支。（旧 ingest_url/ingest_token/sink=ingest 加载期拒绝；新路径 source 信封）

## Workflow follow-up

- 归档前跑 `openspec validate --strict`；实现完成后按仓库门禁收口。
- 本 change 与 longzhong 聚合层 `docs/CONTROL-PLANE-PROJECT.md` 及 `docs/CONTROL-PLANE-BOUNDARY.md` 的对应 WP/边界表保持一致。
