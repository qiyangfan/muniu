# cluster 形态参考部署(6.1)

> 参考样例而非生产清单:两副本 muniu(无状态)+ nginx 轮询 LB + 控制面(锦官/任意
> 实现下述契约端点的服务)。控制面契约三端点:价目下发 / key 吊销快照 / 计量 ingest。
> 镜像自 goreleaser 产物或 `Containerfile` 构建(参见根 README)。

## docker compose(podman compose 同语法)

```yaml
# deploy/cluster/docker-compose.cluster.yml
services:
  lb:
    image: nginx:alpine
    ports: ["8787:8787"]
    volumes:
      - ./nginx.conf:/etc/nginx/nginx.conf:ro
    depends_on: [muniu-a, muniu-b]

  muniu-a: &muniu
    image: ghcr.io/qiyangfan/muniu:v0.3.0   # 或本地构建
    environment:
      - DEEPSEEK_API_KEY_1=${DEEPSEEK_API_KEY_1}     # 上游 key 全副本同源
      - MUNIU_ADMIN_TOKEN=${MUNIU_ADMIN_TOKEN}
    volumes:
      - ./muniu.cluster.yaml:/muniu.yaml:ro          # 拓扑/价目 URL 指向控制面
    # SQLite 仅承载计量缓冲(可重放),生产建议挂本地盘而非共享卷
  muniu-b:
    <<: *muniu

  control-plane:                                       # 锦官或任意契约实现
    image: ghcr.io/qiyangfan/jinguan:latest
    ports: ["8090:8090"]
```

```nginx
# deploy/cluster/nginx.conf (节选)
events {}
http {
  upstream muniu {
    server muniu-a:8787;
    server muniu-b:8787;
  }
  server {
    listen 8787;
    location / {
      proxy_pass http://muniu;
      proxy_http_version 1.1;
      proxy_set_header Connection "";       # SSE 必需:清空 Connection 头保持长连接
      proxy_buffering off;                   # SSE 逐事件透传,LB 不得攒包
      proxy_read_timeout 600s;               # 长流式响应
    }
  }
}
```

## Kubernetes(等价形态)

```yaml
# deploy/cluster/k8s.yaml (节选)
apiVersion: apps/v1
kind: Deployment
metadata: {name: muniu}
spec:
  replicas: 2
  selector: {matchLabels: {app: muniu}}
  template:
    metadata: {labels: {app: muniu}}
    spec:
      containers:
        - name: muniu
          image: ghcr.io/qiyangfan/muniu:v0.3.0
          env:
            - name: DEEPSEEK_API_KEY_1
              valueFrom: {secretKeyRef: {name: muniu-upstream, key: deepseek-1}}
          volumeMounts: [{name: conf, mountPath: /muniu.yaml, subPath: muniu.yaml}]
          readinessProbe: {tcpSocket: {port: 8787}, periodSeconds: 5}
      volumes:
        - name: conf
          configMap: {name: muniu-conf}     # muniu.cluster.yaml 进 ConfigMap
---
apiVersion: v1
kind: Service
metadata: {name: muniu}
spec:
  selector: {app: muniu}
  ports: [{port: 8787, targetPort: 8787}]
```

## 控制面对接(muniu.cluster.yaml 节选)

```yaml
control_plane:
  pricing_url:  "http://control-plane:8090/control/v1/pricing"          # 价目拉取(3.1a)
  topology_url: "http://control-plane:8090/control/v1/topology"         # 拓扑热替换(3.2)
  poll_interval_sec: 60
auth:
  revocations_url: "http://control-plane:8090/control/v1/key-revocations" # 吊销版本快照
metering:
  sink: ingest                      # 计量推控制面(失败本地缓冲续传)
  ingest_url:  "http://control-plane:8090/ingest/usage"
  ingest_token: ${JINGUAN_INGEST_TOKEN}
```

⚠ 注意事项:①LB 必须 `proxy_buffering off`(SSE 逐事件透传);②上游 key 全副本同源,
各实例健康度独立观测(语义见 docs/design/cluster-semantics.md);③副本本地 SQLite 仅
计量缓冲,可重放可丢弃;④拓扑/价目/吊销的收敛窗口 = poll_interval_sec,有界可配。
