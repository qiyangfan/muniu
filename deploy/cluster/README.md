# cluster 形态参考部署(6.1)

## 限流共享视图(ADR-0008 修订)

`muniu.cluster.yaml` 已配 `ratelimit.backend: redis`(compose 内含 redis 服务):RPM/TPM 计数跨副本共享,消除实例独立计数的 ×N 松弛;redis 不可达时各副本自动降级实例自治并告警,恢复后自动回共享视图(宁有弱限制,勿无限制)。日配额的全局权威仍由控制面 ingest 聚合承担。

> 参考样例而非生产清单:两副本 muniu(无状态)+ nginx 轮询 LB + 控制面(锦官/任意
> 实现下述契约端点的服务)。镜像以两仓实际发布渠道为准,或经根 `Containerfile` 构建。

## 工件清单

| 文件 | 用途 |
| --- | --- |
| [docker-compose.cluster.yml](docker-compose.cluster.yml) | compose/podman compose 一键起 LB + 双副本 + 锦官 |
| [nginx.conf](nginx.conf) | LB 配置(SSE 三件套:清 Connection 头 / 关 buffering / 长读超时) |
| [muniu.cluster.yaml](muniu.cluster.yaml) | managed 形态 muniu 配置(统一快照/注册/命令/上报指向锦官) |
| [k8s.yaml](k8s.yaml) | Kubernetes 等价形态(Deployment + Service,配置走 ConfigMap) |

compose 用法:本目录放 `.env`(`DEEPSEEK_API_KEY_1` / `MUNIU_ADMIN_TOKEN` /
`MUNIU_PLATFORM_KEY_SECRET` / `MUNIU_CONTROL_CLIENT_SECRET` / `MUNIU_SERVICE_READ_TOKEN`)
与锦官快照验签公钥 `control-signing.pem` 后
`podman compose -f docker-compose.cluster.yml up -d`。muniu 对配置内 `${VAR}` 缺失是
fail-fast:任一变量缺注入,副本都会拒绝启动。

服务身份前置:Keycloak realm 需有机器 client `muniu-control`(fujie 仓创建,当前阻塞);
本地演练可在 loopback 控制面用 `control_plane.service_token` pre-shared。

## 控制面对接契约

跨仓 wire schema 由 X-01(`2026-11-26-add-control-contract-harness`)冻结;当前锦官新端点
未实施,本仓按 [docs/design/control-plane-client.md](../../docs/design/control-plane-client.md)
与机器可校验副本 [contracts/control-plane-wire.v1.yaml](../../contracts/control-plane-wire.v1.yaml)
实现并以本地契约测试证明,跨仓契约测试标记阻塞。

| 管子 | 端点 | muniu 侧配置 | 认证 |
| --- | --- | --- | --- |
| 控制快照 | `GET /control/v1/snapshot` | `control_plane.mode: managed` | 服务身份 + RSA 验签 |
| 服务注册/心跳 | `POST /control/v1/registry/instances[/{id}/heartbeat]` | managed 自动启用 | 服务身份 |
| 用量/健康 | `POST /ingest/usage` / health 事件 | `metering.sink: control` | 服务身份 |
| 跨服务命令 | `POST /control/v1/commands/claim` + `/{id}/ack` | managed 自动启用 | 服务身份 |
| 服务只读 API | `GET /service/v1/summary`(muniu 提供) | `service_api.issuer` + `clients` | Keycloak `service:read` 服务 token |

收敛与失败:快照 `poll_interval_sec`、心跳 `heartbeat_interval_sec`、命令
`command_poll_interval_sec` 均可配;安全类缺失 fail-closed,非安全类 last-known + stale。

## ⚠ 注意事项

1. LB 必须 `proxy_buffering off`(SSE 逐事件透传),自建 Ingress 同理;
2. 上游 key 全副本同源,各实例 key 池健康度独立观测(语义见
   [docs/design/cluster-semantics.md](../../docs/design/cluster-semantics.md));
3. 副本本地 SQLite 承载计量与缓冲,生产建议挂本地盘而非共享卷;
4. managed 下共享控制写入口本地只读;拓扑/价目/租户变更走锦官。
