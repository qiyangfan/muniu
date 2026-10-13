# Design

## D1 身份形态:本地账号表,不复用 API key 体系、不做无状态签名

管理员是运维面身份,与租户 API key(数据面凭据)语义不同族——复用 `api_keys` 会把「运维动作归因」混进「调用方计量」域。会话采用**服务端表**(`admin_sessions` 存 SHA-256 令牌哈希)而非无状态签名 cookie:签名 cookie 无法服务端主动失效,「停用即踢会话」「改密全注销」就要等自然过期,可追责叙事不成立;standalone 已有本地 SQLite,一条索引查询的成本可忽略。会话令牌 32 字节 `crypto/rand`,cookie `muniu_admin_session`(HttpOnly、SameSite=Lax、Path=/、Max-Age=12h 固定期不滑动);不设 Secure——standalone 一等形态是本地明文 HTTP,TLS 终止归反代(登记边界)。

## D2 迁移与表形态(0012)

```sql
CREATE TABLE admin_users (
  id           TEXT PRIMARY KEY,     -- UUIDv7
  username     TEXT NOT NULL UNIQUE,
  password_hash TEXT NOT NULL,       -- argon2id PHC 串
  status       TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','disabled')),
  created_at   TEXT NOT NULL,
  last_login_at TEXT
);
CREATE TABLE admin_sessions (
  token_hash TEXT PRIMARY KEY,       -- SHA-256(会话令牌);令牌非口令,快速哈希即可
  user_id    TEXT NOT NULL,
  created_at TEXT NOT NULL,
  expires_at TEXT NOT NULL
);
CREATE TABLE admin_audit_log (
  id         TEXT PRIMARY KEY,       -- UUIDv7
  request_id TEXT NOT NULL,
  actor_kind TEXT NOT NULL CHECK (actor_kind IN ('session','token','login')),
  actor_name TEXT NOT NULL,          -- username;token 轨为保留名;login 轨为所试 username
  action     TEXT NOT NULL,          -- 路由模式规整(如 keys.patch / auth.login)
  target_id  TEXT,
  outcome    TEXT NOT NULL,          -- "ok" | "error:<status>" | "denied"
  detail     TEXT,                   -- 脱敏 JSON 摘要
  created_at TEXT NOT NULL
);
CREATE INDEX idx_admin_audit_created ON admin_audit_log(created_at);
```

停用/改密的「即时失效」由删除会话行实现(下一请求查无会话即 401),观测行为与逐请求校验等价,免 join。过期会话惰性清理(命中过期行即删)+ 启动期清扫一次,不做后台任务。

## D3 双轨中间件与 actor 传播

`authAdmin` 改造为双轨:先验会话 cookie,未命中再走现行 Bearer 恒时比较(零改动语义);任一通过即把 actor(kind+name)注入 request context,审计中间件消费。401 信封形态与现行逐字节一致(既有管理面测试零改动是验收线)。管理面挂载条件:`token != "" || 存在活跃管理员`(装配期查一次库;管理员是 DB 态非配置态,SIGHUP 域不含它,与「auth 不热更」现行边界同族)。

## D4 argon2id 与依赖白名单

- 口令哈希 `golang.org/x/crypto/argon2`,参数 OWASP 基线 m=19MiB/t=2/p=1,PHC 串随行存储——参数升级时旧串按自身参数校验,可平滑迁移。校验 = 解析 PHC 重推导 + `subtle.ConstantTimeCompare`。
- 依赖白名单登记:`x/crypto` 为纯 Go 叶子模块(argon2 内部仅依赖同模块 blake2b),与已入列的 `x/time` 同族(golang.org 半标准库);无新增传递依赖。入 ADR-0017 白名单行,`go get` 后跑 `make tidy`。

## D5 登录锁定:进程内存态,诚实边界

同一 username 连续失败 5 次 → 锁 15 分钟(锁定期正确账密也拒,拒绝理由可区分);计数进程内存(map+mutex),重启清零——standalone 单进程,进程即真相;DB 态锁定是为多副本准备的过度设计(集群形态归 ADR-0008 域)。锁定期间审计行照落(outcome=denied)。

## D6 审计中间件:挂点、脱敏、只增不改

- **挂点**:管理面 mux 外层(auth 之后)包一层 statusRecorder;非 GET 路由 + `auth.login` 全量留痕;GET 不留痕(读操作无归因价值,噪声反而是审计敌人)。审计行**同步写** SQLite(管理动作低频;写失败告警不阻断——动作已生效,审计丢失必须可观测)。
- **action 规整**:取路由模式(非原始 path),如 `keys.patch`/`pricing.put`;target 取 path 参数;outcome 由 statusRecorder 回读。
- **脱敏**:detail 为请求体白名单式摘要——`password`/`token`/`secret` 类字段一律 `"<redacted>"`,其余取键名与标量值;未知结构只记键名。
- **只增不改**:无编辑/删除 API;`GET /admin/v1/audit` 按 created_at 倒序游标分页(cursor=末行 id+created_at 复合,避免同秒竞态)。

## D7 管理员管理与引导流

- API:`GET /admin/v1/admins`(无哈希回显)、`POST /admin/v1/admins`(username 唯一、口令 ≥8 字符)、`PATCH /admin/v1/admins/{id}`(`disabled`/`password`;两动作均触发会话清理)。允许停用任何账号含自己——恢复路径恒存在(CLI 直连库或 token 轨),不做「最后一个管理员保护」的伪安全。
- CLI:`muniu admins add|list|disable|enable|passwd`(与 keys CLI 同款直连存储;add 走 tty 双次口令提示,非 tty 收 stdin 供测试)。引导故事:`MUNIU_ADMIN_TOKEN` + `muniu admins add` 或 token 轨 API 创建首个管理员,此后纯会话运维亦可。

## D8 控制台形态

登录页双轨:账密表单为主、「粘贴管理令牌」保留为次要入口(沿用 localStorage token 现状);`GET /admin/v1/auth/whoami` 启动探测,401 路由到登录页。顶栏显示操作员身份与退出;审计页 DataList 表格(actor/action/target/outcome/时间)+ 游标分页。fetch 默认 same-origin 自带 cookie,api.ts 不需要显式 credentials 处理;openapi.yaml 同步五组新端点后 `pnpm generate:api`。
