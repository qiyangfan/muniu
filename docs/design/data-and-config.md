# 存储与配置(data-and-config)

## 分工边界(ADR-0004)

| 类别 | 载体 | 例 |
|---|---|---|
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

## 配置加载(config 包契约)

1. 定位:`-c` 参数 > `MUNIU_CONFIG` > `./muniu.yaml`;
2. 展开:`${VAR}` 引用环境变量,**未定义即启动失败**(fail-fast,沿 kongming compose 纪律);
3. 校验:必填/枚举/URL 合法性/模型路由引用的 provider 必存在/权重正整数——校验错误一次性列出全部问题(不逐个挤牙膏);
4. 启动后配置只读;热加载在 backlog(SIGHUP),v0 不做。

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
- 明确不做:定时/自动备份(OS 层 cron/launchd/Time Machine 的职责)、云同步、加密、
  增量备份、cluster 形态(无状态副本 + jinguan PG 自有备份生态,ADR-0005 边界)。
