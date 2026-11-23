# 存储与配置(data-and-config)

## 分工边界(ADR-0004)

| 类别 | 载体 | 例 |
| --- | --- | --- |
| 静态拓扑(部署即定,git 可审计) | `muniu.yaml` | providers/model_routes/pricing/超时 |
| 运行态(运行中增删改) | 内嵌 SQLite | tenants/api_keys/usage_logs/(Phase 4)语义缓存 |
| 秘密 | 环境变量 | provider keys(YAML/DB 只存引用名) |

## SQLite Schema v0.1(迁移 0001,DDL 为契约)

```sql
-- 0001_init.sql
CREATE TABLE schema_migrations (
  version    INTEGER PRIMARY KEY,
  applied_at TEXT NOT NULL
);

CREATE TABLE tenants (
  id         TEXT PRIMARY KEY,                 -- UUIDv7 字符串
  name       TEXT NOT NULL UNIQUE,
  status     TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','disabled')),
  created_at TEXT NOT NULL                      -- RFC3339 UTC
);
-- 种子:INSERT INTO tenants(id,name,...) VALUES('...','default',...)——随迁移写入

CREATE TABLE api_keys (
  id        TEXT PRIMARY KEY,
  tenant_id TEXT NOT NULL REFERENCES tenants(id),
  name      TEXT NOT NULL,
  key_hash  TEXT NOT NULL UNIQUE,               -- sha256 hex,明文绝不落盘
  prefix    TEXT NOT NULL,                      -- 明文前 12 字符(mk-XXXXXXXX)供列表辨认
  status    TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','revoked')),
  rpm_limit        INTEGER NOT NULL DEFAULT 60,
  tpm_limit        INTEGER NOT NULL DEFAULT 0,  -- 0=不限(Phase 3 生效硬闸)
  daily_quota_credits INTEGER NOT NULL DEFAULT 0, -- 0=不限;credit 单位(1 credit = 本位币 1e-9),迁移 0007 重命名换算
  currency         TEXT NOT NULL DEFAULT 'CNY' CHECK (currency IN ('CNY','USD')),
  expires_at       TEXT,                        -- 迁移 0006;可空=NULL 永不过期;RFC3339;过期判定在鉴权层逐请求以当前时钟执行
  disabled_at      TEXT,                        -- 迁移 0009;可空=NULL 启用;可逆停用(吊销仍是唯一终态,判定先吊销后停用)
  max_request_cost_credits INTEGER NOT NULL DEFAULT 0, -- 迁移 0010;单请求成本预算,0=不限(预检+流中两道闸)
  created_at TEXT NOT NULL,
  revoked_at TEXT
);

CREATE TABLE usage_logs (
  id         TEXT PRIMARY KEY,
  request_id TEXT NOT NULL,
  tenant_id  TEXT NOT NULL,
  api_key_id TEXT NOT NULL,
  provider       TEXT NOT NULL,
  model_alias    TEXT NOT NULL,
  upstream_model TEXT NOT NULL,
  stream      INTEGER NOT NULL CHECK (stream IN (0,1)),
  status      TEXT NOT NULL CHECK (status IN ('ok','error','client_cancel')),
  error_class TEXT,
  prompt_tokens     INTEGER NOT NULL DEFAULT 0,
  completion_tokens INTEGER NOT NULL DEFAULT 0,
  reasoning_tokens  INTEGER NOT NULL DEFAULT 0,
  cached_tokens     INTEGER NOT NULL DEFAULT 0,
  vision_tokens     INTEGER NOT NULL DEFAULT 0, -- 图像/视频 token 子集,迁移 0014
  estimated   INTEGER NOT NULL DEFAULT 0 CHECK (estimated IN (0,1)),
  cost_credits INTEGER NOT NULL DEFAULT 0, -- credit 单位(1 credit = 本位币 1e-9),迁移 0007 重命名换算
  currency    TEXT NOT NULL DEFAULT 'CNY',
  latency_first_token_ms INTEGER,
  latency_total_ms       INTEGER NOT NULL,
  created_at  TEXT NOT NULL
);
CREATE INDEX idx_usage_tenant_time ON usage_logs(tenant_id, created_at);
CREATE INDEX idx_usage_key_time    ON usage_logs(api_key_id, created_at);
CREATE INDEX idx_usage_day         ON usage_logs(created_at);   -- 日配额聚合
```

(Phase 4 追加 `semantic_cache` 表于迁移 0002+,不在 0001 预建——「已规划能力一次建全」适用于同一能力域,语义缓存届时按当时设计定列。)

## 迁移机制

`internal/store/migrations/*.sql` embed;启动时顺序执行未应用文件,单事务一文件;`PRAGMA journal_mode=WAL, busy_timeout=5000, foreign_keys=ON` 随建库执行。**已应用文件禁改**,修 schema 只追加新迁移。

迁移 0014 追加 `usage_logs.vision_tokens` 与 `model_pricing.vision_input_credits`;默认 0 保持旧用量行和旧价目兼容,
视觉价 0 表示沿用 input 价。

## 配置加载(config 包契约)

1. 定位:`-c` 参数 > `MUNIU_CONFIG` > `./muniu.yaml`;
2. 展开:`${VAR}` 引用环境变量,**未定义即启动失败**(fail-fast,沿 kongming compose 纪律);
3. 校验:必填/枚举/URL 合法性/模型路由引用的 provider 必存在/权重正整数——校验错误一次性列出全部问题(不逐个挤牙膏);
4. 启动后配置只读;SIGHUP 重新加载并校验静态拓扑后原子替换 provider/model_routes,失败时保持旧快照。

### 百炼原生多模态向量

`providers[].native_multimodal_embeddings_url` 是可选的完整 http(s) URL,仅 `adapter: qwen` 可声明;
该字段随本地 YAML、SIGHUP 与控制面拓扑同步,未声明时该 provider 不参与原生多模态向量候选。
支持范围固定为 `qwen3-vl-embedding` 与 `tongyi-embedding-vision-flash` 当前快照;新 Tongyi 快照不在本约定内。

网关新增 `POST /api/v1/services/embeddings/multimodal-embedding/multimodal-embedding`,请求体上限 16 MiB。
请求采用百炼原生 JSON,要求非空 `model` 路由别名与非空 `input.contents`;其余内容逐字节保留。
`parameters`、图片/视频 URL 可访问性、格式、尺寸、数量、维度与融合限制均由百炼校验,网关不补默认值、
不静默删改参数,并透传原生响应与上游错误。

token 价目可选 `vision_input`,单位仍是 credit/百万 token;缺省回落 `input`。视觉 token 是 `prompt_tokens` 的子集。

## 语义缓存配置(Phase 4 落地,`semantic:` 节)

`enabled`(总开关,租户级开关另需 tenants.semantic_cache_enabled,迁移 0003)·
`embedding_model`(模型别名,嵌入调用经本网关 /v1/embeddings)· `dim`(缺省 1536)·
`threshold`(缺省 0.97)· `ttl_hours`(缺省 24)· `capacity`(缺省 10000,LRU)。
缺省值与校验实现在 internal/config;一致性核对 2026-09-10:文档与实现一致。

## 状态备份与恢复(openspec `state-backup`,2026-09-30)

standalone 形态的全量运营态(key 哈希/用量账本/价目)在单文件 `storage.sqlite.path`,
备份即这一状态的完整快照——**恢复是离线运维动作**(停服 → 恢复 → 起服),不承诺热切换。

- **备份**(`muniu backup [--output DIR]`,在线可执行):`VACUUM INTO` 取一致快照(WAL 下
  不阻塞读写;纯 SQL,不引 backup API、零新增依赖);产物 = `{muniu.db, manifest.json}`
  (版本/迁移版本/时刻 UTC/api_keys 与 usage_logs 行数/sha256);快照后
  `PRAGMA integrity_check`,不过即删档报错(坏档不残留);目录 0700。
- **恢复**(`muniu restore <backup-dir> [--target PATH] [--force]`):四道闸依序——
  ① sha256 与 manifest 一致;② 快照 integrity + 行数核对;③ 目标库被实例持锁即拒
  (SQLite 连接持有期 db 与 `-shm` 均带 POSIX 锁,实测空闲连接亦然,进程退出即释放);
  ④ 目标已存在需 `--force`。落位走临时文件 + rename,覆盖前保留现场,恢复后复校验,
  不符自动回滚。
- **manifest 的 usage_logs 行数 = 月末对账的本地锚点**(T5.3 灰度判据的物质载体)。
- 备份语义为**语句开始时刻的一致快照**:快照内账本自洽,不要求与备份后到达的写入一致。
- **备份含敏感凭据(oauth-subscription-lane,ADR-0021 披露)**:OAuth 订阅账号的
  access/refresh token 以明文落库(刷新需原文使用,无法哈希),随备份档一并带走;
  备份目录 0700 是唯一访问闸,备份档的存放与流转按敏感凭据同级对待。
- 明确不做:定时/自动备份(OS 层 cron/launchd/Time Machine 的职责)、云同步、加密、
  增量备份、cluster 形态(无状态副本 + jinguan PG 自有备份生态,ADR-0005 边界)。

## 图片价目与管理面身份（ADR-0016/0017/0022）

追加迁移 0011 `image_pricing(alias TEXT PK,currency TEXT CNY|USD,default_credits INTEGER>0,tiers TEXT JSON,updated_at TEXT)`。tiers 为 `size|quality → credit/张` 对象；读取期严格解析，损坏拒启/拒刷新。配置 `image_pricing` 是按别名组织的静态报价映射（currency/default/tiers 字符串），与既有 pricing 平行。

追加迁移 0012 的三表在 ADR-0017 时代为本地账密模型；木牛去账号化（ADR-0022，虎符为唯一账户权威）后追加迁移 0021 收敛为 subject 主键模型：

- `admin_principals(subject TEXT PK,username TEXT,status TEXT active|disabled,created_at TEXT,last_login_at TEXT nullable)`：subject 为虎符规范 sub（issuer+sub），是唯一身份键；username 只是 id_token 展示名，同名不同 sub 可并存。主体只能由虎符首登自动建档，不再保留 `password_hash` 作为登录凭据。
- `admin_sessions(token_hash TEXT SHA-256 PK,subject TEXT FK→admin_principals,created_at TEXT,expires_at TEXT)`：令牌 32 随机字节，固定 12h、HttpOnly、SameSite=Lax、Path=/；逐请求 JOIN 主体状态，停用即删该主体全部会话且签发时条件核验活跃状态。
- `admin_audit_log` 不变（actor_kind 值域为 session/token/login/federated；session 仅历史行兼容保留，login 用于登录事件与匿名动作行（ADR-0023），人员会话行用 federated），按 created_at/id 倒序复合游标查询，只增不改；变更同步留痕，写失败告警，动作不回滚。
- 迁移纪律：零存量重建——生产从未建过本地管理员；旧库 `admin_users` 含本地账密行时装配期启动闸 `store.EnsureNoLocalAdminAccounts` 指名行并拒绝启动，不静默丢行。旧表与联邦影子 id 列保留但不读写。

管理面挂载条件为共享令牌、存在活跃主体或虎符联邦已声明；现行 Bearer 轨保留，actor 为 management-token。`muniu admins list|disable --subject|enable --subject` 直连同库；`POST /admin/v1/auth/logout`、`GET /admin/v1/auth/whoami`、`GET /admin/v1/auth/federation`、`GET /admin/v1/auth/oidc/start|callback`、`GET /admin/v1/admins`、`PATCH /admin/v1/admins/{subject}`（路径参数即 subject）、`GET /admin/v1/audit` 是管理 API；本地账密 `POST /admin/v1/auth/login` 与 `POST /admin/v1/admins` 已删除。数据面 API key 与运维身份分离。

登录失败锁定、口令哈希、改密注销等本地账密硬化随账密轨一并移除；会话失效只剩两条：主体停用（即时）与到期/注销。过期会话启动清扫/命中惰性清理，不增加后台任务。会话轨使用 Go 标准库 CrossOriginProtection 拒绝跨站写操作；Bearer 脚本轨行为保留。管理面 cookie 的 Secure 由顶层 `admin_cookie_secure` 决定（缺省按 `admin_oidc.issuer` 是否 https 推导），反代终止 TLS 时必须显式开启；state 与登录发起另有每 IP 限流。审计 body 只取允许的业务标量，password/token/secret 脱敏，未知字段只留字段名。管理请求体上限 1MiB。

`GET /metrics` 仍独立使用 Bearer token 门禁，不因主体轨引入而开放；身份与静态报价仍不属于 SIGHUP 热更域。

供应商管理页与 keypool gauge 在每次读取时复用 Proxy.Topology() 的当前同一快照，不持启动时 providers/pools 作为热更后的真相；无动态视图注入的管理 handler 保留静态形态用于独立调用与测试。
