# Design

## Context

现状锚点(代码事实):

- `admin_principals(subject PK, username, status, created_at, last_login_at)` 无角色列(迁移 0021);
  共享令牌轨与联邦会话轨都直接获得全部管理面能力。
- 管理面路由在 `adminRouteTable` 单点声明(注册与 `Routes()` 回报共用),中间件链为
  `authOperators(auditOperators(mux))`;审计记录非 GET 变更与 401/403 denied。
- `whoami` 返回 `Actor{kind,name}`,不含 subject/scope;`GET /admin/v1/admins` 无范围字段。
- `usage` 管理查询已透传 `tenant_id` 参数到 `UsageAggregate`,即当前任何管理员可用任意租户;
  `listKeys` 全量返回;`ListTenants` 全量返回。
- 数据面租户轴已完好:key→tenant、租户停用即拒鉴权、按 key/租户限流,本件零触碰。

跨仓决策(粗角色在 IdP、细范围在产品/策略层、不自建 PDP)见
`longzhong/docs/adr/0002-authorization-strategy.md`;本设计只记录木牛执行决定。

## Goals / Non-Goals

Goals:管理面出现"租户级操作员"这一真实档位;范围判定单点收敛、失败关闭;全局轨
(共享令牌、存量主体)行为零变化;为 OPA 对照留出唯一 seam。

Non-Goals:OPA 实现、Keycloak 角色映射、锦官改动、数据面改动、通用权限点系统、CLI 扩展。

## Decisions

1. **范围是显式列,不从绑定有无反推**。`scope ∈ {GLOBAL, TENANT}` 落在 `admin_principals`;
   "无绑定"不得被解释为"全局"(缺省即最大权限是经典 fail-open)。未知取值一律拒绝。
2. **默认最小权 + 兼容迁移**。新联邦主体首登建档为 `TENANT` + 空绑定,执行侧 fail-closed
   (仅 `whoami`/`logout`/认证入口可达);存量主体迁移 backfill 为 `GLOBAL`(保持既有行为);
   共享令牌轨恒 `GLOBAL` 作为 break-glass 与首个授权入口。该次序同时解决
   "新库无人可授权" 与 "升级即锁死老管理员" 两个方向的风险。
3. **路由分类与注册同源**。`adminRouteTable` 条目增分类字段(global-only / tenant-scoped
   与租户提取函数),结构体必填使新增路由无法"忘记分类";未分类仍按 global-only 处理
   (对 scoped 主体拒绝,对 GLOBAL 放行)。分类是编译期约束而不是运行时约定。
4. **单点执行,拒绝可追责**。scope 中间件串在审计内侧:`auth(audit(scope(mux)))`,
   scope 拒绝产生 403 且进入审计 denied(任意方法,含 GET);非拒绝 GET 不新增审计。
   所有资源级判定收敛到单一函数 `AuthorizeScope(actor, class, resourceTenant)`,资源
   处理器只消费判定结果。OPA 对照时只替换该函数实现。
5. **读侧过滤 + 写侧判定,越界按不存在**。列表按绑定集合过滤(在存储查询内完成,不在内存
   截断);查询参数携带范围外租户、写操作目标 key 属于范围外租户,一律 `404 not_found`
   (不披露范围外资源的存在性),不写库、留审计。`usage` 缺省 = 绑定并集;显式参数必须是
   绑定子集。多绑定场景聚合用 `tenant_id IN (...)`,不循环单租户查询。
6. **授予权只属于 GLOBAL**。`PATCH /admin/v1/admins/{subject}` 纯增 `scope` 与
   `tenant_ids`(整包替换,省略=不动);`scope=GLOBAL` 携带非空 `tenant_ids` → 400;
   `tenant_ids` 含不存在租户 → 400;`scope=TENANT` 允许空集(待授权态,执行侧关闭);
   TENANT actor 调用授予 → 403,不落库。
7. **IdP 角色与产品范围边界**。Keycloak 角色只决定"能否进入管理面"(粗粒度,后续接入变更);
   `scope`/绑定是产品数据(细粒度不塞 token,与 IAM 目标一致)。当前三条鉴权轨(联邦会话/
   共享令牌/历史行)不新增第四条。
8. **控制台最小适配**。前端以 `whoami.scope` 过滤导航:GLOBAL 保持现有全部页签;`TENANT`
   仅 Usage / Keys(Overview 的按日用量虽已收敛,但缓存统计与告警投递是 global-only 源,
   403 后被既有降级逻辑静默显示为「未启用/无告警」,构成误导,故整页不纳入 TENANT 视图),
   TENANT 默认落地 Usage;Keys 页租户输入改从 `GET /tenants` 下拉(读侧已按 scope 收敛,
   前端不复制授权逻辑)。显示只读范围徽标,403 走既有 ErrorAlert;不做租户页与绑定编辑
   界面(授予走 API)。
9. **不引入缓存与指标**。范围解析随会话/请求读一次主体行(管理面基数极小),不新增缓存层;
   拒绝已由审计承载,不新增 Prometheus 指标,避免把授权判断做成可观测性工程。

## Risks / Trade-offs

- [迁移默认值写错导致存量管理员锁死] → backfill GLOBAL + 迁移行级断言 + 全局轨零回归测试。
- [新增路由漏分类] → 分类字段必填 + 覆全路由表的断言测试;运行时默认 global-only 只作兜底。
- [双事实(IdP 角色 vs 产品 scope)] → 角色管入口、scope 管执行,边界写入本设计与
  admin-tenant-scope 规格;两者不互相推导。
- [管理面列表全量后内存过滤的性能] → 过滤下推存储查询;管理面数据量小,不预建索引优化;
  若 key 规模超预期,再按租户索引评估,不在本件做。
- [console 隐藏 ≠ 授权] → 前端过滤只是体验;执行侧 403/404 是唯一授权事实,负向测试钉住。

## Migration Plan

迁移 0023 纯增量:加 `scope` 列(默认 `TENANT`)与 `admin_principal_tenants` 表,随后
`UPDATE admin_principals SET scope='GLOBAL'` 将存量行统一提为全局(此刻表中不存在新主体,
无时间窗歧义)。回滚 = 代码回滚(列/表保留,遵守只增纪律),数据无损失;greenfield 可直接重置。

## Open Questions

- 控制台是否后续需要租户只读页与绑定编辑界面——不影响执行语义,推后;租户列表读侧 API 已就绪。
- CLI 授予是否随 Keycloak 角色映射一起做——本件先用管理 API;届时角色与范围的关系在接入变更内定。
