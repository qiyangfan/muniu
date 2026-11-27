# Tasks

## 1. 执行

- [x] 1.1 租户配额读取/叠加判定。验证：租户/key 双上限。（租户 RPM/TPM/日配额 + key；TestGate_租户RPM不被key上限掩盖 / key上限独立 / 租户TPM）
- [x] 1.2 缺失/非法 fail-closed。验证：安全类拒绝。（快照未就绪 503；租户不在目录 403）

## 2. 审计与观测

- [x] 2.1 拒绝审计/指标。验证：可追责。（muniu_quota_denials_total + request_id/tenant/key 结构化日志）

## 3. 验收与独立运行

- [x] 3.1 权限负向与跨租户用例。验证：未授权 401/403，跨租户 404/不可见。（既有 scope 测试；租户目录外 403）
- [x] 3.2 审计断言。验证：每次写操作有审计记录；普通列表只读不写审计；敏感详情/导出按审计策略。（配额为读路径：拒绝以指标+日志留痕，不写审计洪水）
- [x] 3.3 独立运行/降级 smoke。验证：停掉管理面或其他仓，本服务核心流程不受影响；管理面停服仅局部不可用。（managed fail-closed / last-known e2e）
- [ ] 3.4 契约与生成客户端。验证：OpenAPI/生成物无漂移；跨仓契约测试必跑（X-01）。**阻塞：X-01 未冻结。**
- [x] 3.5 Greenfield 重置。验证：无兼容/双轨分支。（managed 租户写入口关闭；租户配额以快照为准投影）

## Workflow follow-up

- 归档前跑 `openspec validate --strict`；实现完成后按仓库门禁收口。
- 本 change 与 longzhong 聚合层 `docs/CONTROL-PLANE-PROJECT.md` 及 `docs/CONTROL-PLANE-BOUNDARY.md` 的对应 WP/边界表保持一致。
