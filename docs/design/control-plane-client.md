# 控制面客户端:身份 · 快照 · 注册 · 命令 · 上报(control-plane-client)

> 本文件描述 muniu 作为数据面消费锦官共享控制的**行为契约**。跨仓 wire schema 的
> 最终权威是 X-01(`2026-11-26-add-control-contract-harness`);该契约尚未冻结前,
> 本仓按本文实现并以本地测试证明,跨仓契约测试标记阻塞。字段变更必须双方 change 同步,
> 禁止单边漂移。机器可校验副本见
> [contracts/control-plane-wire.v1.yaml](../../contracts/control-plane-wire.v1.yaml)(提案 v1)。

## 1. 双形态与配置

- `control_plane.mode: standalone`(缺省):完全关闭控制面循环;价目/拓扑/租户走本地配置与管理面。
- `control_plane.mode: managed`:统一快照 + 注册心跳 + 命令 claim + 用量/健康上报。
- managed 必填:`base_url`、服务身份(`service_token` 或 `token_url/client_id/client_secret`)、
  `snapshot_public_key_file`。pre-shared `service_token` **仅限 loopback**;非本机控制面必须用
  Keycloak client credentials。
- 旧配置键(`control_plane.pricing_url/topology_url`、`auth.revocations_url`、
  `metering.ingest_url/ingest_token`、`metering.sink: ingest`)**已退役**,加载期拒绝。
- 控制面不可达不阻塞启动与数据面;控制面配置缺失/非法属于启动期错误。

```yaml
control_plane:
  mode: managed
  base_url: "https://jinguan.example.com"
  token_url: "https://keycloak.example.com/realms/longzhong/protocol/openid-connect/token"
  client_id: "muniu-control"
  client_secret: "${MUNIU_CONTROL_CLIENT_SECRET}"
  snapshot_public_key_file: "/etc/muniu/control-signing.pem"
  poll_interval_sec: 60
  heartbeat_interval_sec: 30
  command_poll_interval_sec: 15
  request_timeout_sec: 10
service_api:
  token: "${MUNIU_SERVICE_READ_TOKEN}"
```

## 2. 服务身份

- 出站:所有控制面请求带 `Authorization: Bearer <service token>`。
  - client credentials 短时 token,缓存至到期前 30s;
  - pre-shared 仅本地演练,配置校验拒绝非 loopback base_url。
- 入站服务只读 API:`service_api.token` 恒时比较;未配置即不挂载。
- 凭据只存内存,日志/payload 不回显;注册 payload 不含 endpoint、凭据或服务域配置。

## 3. 统一快照

- 端点:`GET /control/v1/snapshot`,带 `If-None-Match` 条件请求;304 视为可达。
- 响应头:`ETag`、`X-Control-Signature`(RSA-SHA256 PKCS#1 v1.5,对**原始响应体**签名)。
- 响应体:

```json
{
  "version": 1,
  "observed_at": "2026-11-27T00:00:00Z",
  "payload": {
    "pricing": [{"alias":"m","currency":"CNY","input":"2.00","cached_input":"","vision_input":"","output":"8.00","timezone":"UTC","time_windows":[]}],
    "image_pricing": [{"alias":"img-a","currency":"CNY","default":"0.50","tiers":{"1024x1024|standard":"1.00"}}],
    "topology": {"providers":[],"model_routes":{},"models":[]},
    "revocations": {"version":1,"revoked":[]},
    "tenants": [{"id":"...","name":"default","status":"active","rpm_limit":0,"tpm_limit":0,"daily_quota_credits":0}],
    "policies": null
  }
}
```

- 版本单调:低于当前版本的响应拒绝;`pricing`/`topology` 缺席表示保留现状,`[]`/空对象表示显式清空。
  吊销名单版本同样单调:低于已应用版本即整包拒绝,防止回退快照落 last-known 后在重启时复活已吊销 key。
- `image_pricing` 同样按「缺席保留、`[]` 清空、非空整表替换」处理;金额与档位逐条解析并换算本位币,
  任一条非法整包拒绝,不做部分应用。
- 应用顺序:先构建价目表/候选拓扑/租户配额,再单事务落库(价目整表替换、租户 upsert 且缺席停用、
  last-known + 审计),最后切换内存目录;任何一步失败保留旧目录。
- 租户身份以控制面 ID 为准:managed 本地租户表只是投影。同名不同 ID 的本地残留无引用
  (`api_keys`/`semantic_cache`/管理员租户绑定均为零)时随快照事务删除;有引用则整包拒绝并报
  `tenant_identity_conflict`,需人工迁移,禁止静默改写历史归属。
- 安全类 fail-closed:`revocations` 或 `tenants` 缺失、签名无效、JSON 非法 → 整包拒绝。
  managed 下未收到有效吊销快照前平台 key 一律拒绝;租户配额快照未就绪时数据面 503;
  租户 `status=disabled` 时数据面 403 `tenant_disabled`(命中快照即时生效,含平台 key)。
- 非安全类 last-known:控制面缺席时用本地 last-known 起服,状态标 `stale`,数据面继续。
- `policies` 只做投影与计数,不进入转发热路径。

## 4. 注册与心跳

- `POST /control/v1/registry/instances`(幂等注册)与
  `POST /control/v1/registry/instances/{instance_id}/heartbeat`。
- payload:`service_id=muniu`、`instance_id`、`version`、`capabilities`、`entitlement_keys`、
  `admin_api`、`started_at`;心跳另带真实 `health`(source/status/metrics)。
- 失败指数退避(1s→60s),恢复后重新注册并补报;不阻塞数据面。
- 不伪造 ready:健康只由 DB ping/资源探针给出,未知时 `unknown`。
- 状态迁移(ok↔degraded)写系统审计;逐心跳不写库,只留结构化日志。

## 5. 用量与健康

- `metering.sink: control`(managed 缺省):先写本地 `usage_logs`,再以服务身份
  `POST /ingest/usage` `{"source":"muniu","schema_version":1,"rows":[UsageRow...]}`。
- 推送失败返回错误 → 计量管道整批落 `metering_buffer`,恢复后按序续传;行主键幂等。
- LLM token/费用只由 muniu 产生;健康事件独立于计费行。

## 6. 跨服务命令

- `POST /control/v1/commands/claim` 拉取(带 service/instance);`POST /control/v1/commands/{id}/ack`
  回执 `acked|failed`。
- 有限动作枚举:`cache_purge`(参数 `all=true` 或 `tenant`,二选一)、`topology_reload`(仅 standalone,空参数)。
- 未知 action/schema_version/参数 → `failed` 回执且不执行;命令 ID 幂等,已终态不重复执行;
  claim 后先在本地台账落 `pending` 再执行,终态更新失败不回执、等重投;ack 失败重试上限 5 次。
- 本地运行命令由内嵌控制台直接调用同一执行器,不经队列;锦官不可用仍可执行。
- 执行结果写 `control_commands` 台账,跨服务命令另写系统审计。

## 7. 只读投影与管理面边界

- `GET /service/v1/summary`(服务身份):返回服务域计数、运行时长、快照/注册/命令状态;
  不含凭据、token、连接串或服务域配置原值。
- managed 下本地价目/租户/拓扑写入口 403;`SIGHUP` 拓扑重载被忽略。
- key 签发仍在本地，但 managed 下必须先获得有效快照/last-known（否则 503
  `tenant_directory_unavailable`），且绑定租户必须命中快照租户目录并为 `active`。
- 运行时页:`GET/POST /admin/v1/runtime/commands`、`GET /admin/v1/runtime/{status,shared-control,logs}`;
  GLOBAL 主体可访问,GET 不写审计,命令写操作走统一审计。

## 8. 失败矩阵(摘要)

| 缺失/故障 | 数据面 | 控制能力 |
| --- | --- | --- |
| 锦官不可达 | last-known 继续;无 last-known 时租户配额 fail-closed(503) | 注册/心跳/命令退避重试 |
| 快照签名/结构非法 | 保留旧目录 | 记录 last_error,不推进版本 |
| 吊销快照缺失(managed) | 平台 key 拒绝;本地查库 key 可用 | — |
| 服务身份失效 | 数据面不受影响 | 控制面调用 401/403,沿用 last-known |
| 本地命令(锦官不可用) | 正常 | 内嵌控制台直接执行并审计 |
