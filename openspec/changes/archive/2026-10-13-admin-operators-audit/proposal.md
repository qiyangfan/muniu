# Proposal

## Why

管理面现状是「一把共享 `MUNIU_ADMIN_TOKEN` + 审计零归因」:谁吊销了这把 key、谁改的价目、谁关的插件,事后无从回答。2026-10-13 二次裁决把「控制台多用户」从 SSO 捆绑中拆出改判引入:ADR-0005 已判 standalone 是一等公民形态,该形态下网关管理面就是最终形态(没有锦官兜底),运维面从「能操作」升到「可追责」是真实完整度缺口;one-api/Gitea/Grafana 同类全部具备本地管理账号。SSO/OIDC 本身维持不引入(组合冗余——OIDC 学习主场在锦官;standalone 零外联纪律;论证已入 HANDOFF 开放决策项)。

## What Changes

- 本地管理员账号:新增 `admin_users` 表(username 唯一、argon2id PHC 串口令哈希、active/disabled 软停用),CLI `muniu admins add/list/disable/enable/passwd` 引导首个账号(与 keys CLI 同款直连存储形态)。
- 会话认证:`POST /admin/v1/auth/login` 签发 HttpOnly 会话 cookie(`admin_sessions` 表存 SHA-256 令牌哈希,12h 固定期),logout 注销;管理面中间件接受「会话 cookie 或 Bearer token」双轨——`MUNIU_ADMIN_TOKEN` 现行路径原样保留(CLI/脚本兼容,契约零破坏),两者皆识别为具名 actor。
- 登录硬化:argon2id 参数随 PHC 串存储可迁移;恒时比较;连续失败锁定(进程内存计数,5 次 → 15 分钟,重启清零为登记边界);登录成功/失败均入审计。
- 管理动作审计:`admin_audit_log` 表全量留痕管理面变更动作(actor/method 规整 action/target/outcome/脱敏 detail/request_id),登录事件同表;`GET /admin/v1/audit` 游标分页查询。管理面开关条件从「token 已配置」放宽为「token 已配置或存在活跃管理员」。
- 管理员管理 API:`GET/POST /admin/v1/admins`、`PATCH /admin/v1/admins/{id}`(停用即时踢会话、改密注销全部会话);审计查询只读。
- 控制台:登录页(账密 + 保留管理令牌粘贴双轨)、顶栏操作员身份与退出、审计页(表格 + 分页)。

## Capabilities

### New Capabilities
- `admin-operators`:管理面身份与审计契约——本地管理员账号生命周期、会话认证与 token 双轨兼容、登录硬化纪律、管理动作审计归因与查询。

### Modified Capabilities

(无——既有规格不含管理面鉴权行为;`platform-data-plane` 的 SIGHUP 等数据面能力不受影响。)

## Impact

- 迁移:0012 新增 `admin_users`/`admin_sessions`/`admin_audit_log` 三表(追加式)。
- 依赖白名单:新增 `golang.org/x/crypto`(argon2id;纯 Go 叶子依赖,同族 `x/time` 先例;登记于 ADR-0017)。
- 代码:`internal/auth` 或新 `internal/adminauth`(账号/会话/审计存储与服务)、`internal/admin`(中间件双轨 + auth/admins/audit 路由)、`cmd/muniu`(admins 子命令)、web(登录页/审计页)。
- 契约兼容:既有 Bearer token 管理调用与控制台令牌登录零破坏;管理面鉴权失败的 401 信封形态不变。
- 明确不做:角色矩阵/组织架构(单角色=管理员)、密码找回、SSO/OIDC、审计外发(webhook 审计归 alerting 域另行裁决)。
