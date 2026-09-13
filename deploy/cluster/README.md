# cluster 形态参考部署(6.1)

> 参考样例而非生产清单:两副本 muniu(无状态)+ nginx 轮询 LB + 控制面(锦官/任意
> 实现下述契约端点的服务)。镜像以两仓实际发布渠道为准,或经根 `Containerfile` 构建。

## 工件清单

| 文件 | 用途 |
|---|---|
| [docker-compose.cluster.yml](docker-compose.cluster.yml) | compose/podman compose 一键起 LB + 双副本 + 锦官 |
| [nginx.conf](nginx.conf) | LB 配置(SSE 三件套:清 Connection 头 / 关 buffering / 长读超时) |
| [muniu.cluster.yaml](muniu.cluster.yaml) | cluster 形态 muniu 配置(三管子指向锦官真实端点) |
| [k8s.yaml](k8s.yaml) | Kubernetes 等价形态(Deployment + Service,配置走 ConfigMap) |

compose 用法:本目录放 `.env`(`DEEPSEEK_API_KEY_1` / `MUNIU_ADMIN_TOKEN` /
`JINGUAN_INGEST_TOKEN` / `MUNIU_PLATFORM_KEY_SECRET`,base64 解码后 ≥16 字节)后
`podman compose -f docker-compose.cluster.yml up -d`。注意 muniu 对配置内 `${VAR}`
缺失是 fail-fast:四个变量任一缺注入,副本都会拒绝启动。

## 控制面对接契约(锦官已实现端点;联调实录 2026-09-13)

| 管子 | 端点(锦官) | muniu 侧配置 | 认证 |
|---|---|---|---|
| 价目下发 | `GET /control/v1/pricing` | `control_plane.pricing_url` | 无认证头(部署网络隔离保护,现实契约;拉取认证为双方演进项) |
| key 吊销 | `GET /control/v1/key-revocations` | `auth.revocations_url` | 同上 |
| 计量 ingest | `POST /ingest/usage` | `metering.sink: ingest` + `ingest_url` | Bearer 独立静态 token(`${VAR}` 展开) |
| 拓扑下发 | `GET /control/v1/topology` | `control_plane.topology_url` | 无认证头(同价目/吊销);录入走锦官 `PUT /admin/topology`(ADMIN) |

拓扑管子联调实录(独立实例,`poll_interval_sec=2`):首拉热替换 `reused=0/created=1/removed=1`;
变更轮新增 alias 后 `reused=1/created=0/removed=0`(provider 配置未变,池运行态无损保留);
锦官停机后 WARN `控制面拓扑拉取失败,保持当前快照`,数据面持续存活——ETag 条件请求、
变更热替换、失联保持三语义均实证。

## ⚠ 注意事项

1. LB 必须 `proxy_buffering off`(SSE 逐事件透传),自建 Ingress 同理;
2. 上游 key 全副本同源,各实例 key 池健康度独立观测(语义见
   [docs/design/cluster-semantics.md](../../docs/design/cluster-semantics.md));
3. 副本本地 SQLite 仅承载计量缓冲,可重放可丢弃,生产建议挂本地盘而非共享卷;
4. 拓扑/价目/吊销的收敛窗口 = `poll_interval_sec` / `revocations_interval_sec`,有界可配。
