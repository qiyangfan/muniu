# Design: 服务注册客户端（service-registration-client）

## Context

锦官 service-registry 已定义接收面；muniu 缺少注册/心跳客户端，控制台无法获得实例、能力、版本和健康。注册是 push 契约，与快照 pull 契约分离；standalone 没有控制面，注册应可禁用。

## Goals / Non-Goals

**Goals:**

- managed 服务身份注册/心跳/能力/版本上报。
- 控制面缺席不阻塞启动与数据面转发。
- 幂等、退避重试、恢复补报。

**Non-Goals:**

- 不做服务发现/负载均衡。
- 不做控制面 UI。
- 不写共享控制数据。
- 不把注册当健康伪造。

## Decisions

1. 注册/心跳使用服务身份 + TLS；payload 含 service_id/instance_id/version/capabilities/admin API 能力/权益键。
2. managed 默认启用；standalone 通过配置禁用；控制面缺席不阻塞启动和数据面请求。
3. 启动异步注册，后台周期心跳；失败指数退避，锦官恢复后补报。
4. instance_id + 幂等键去重；重复上报只更新 last_seen/能力，不创建重复实例。
5. 能力与版本来自代码/配置单一来源；能力键与锦官 service-registry 契约一致。
6. 不上报凭据、连接串、服务域配置或服务只读 API endpoint；服务地址由锦官静态配置；注册失败不改变本地配置。

## Risks / Trade-offs

- 心跳丢失 → 控制面 stale，不自动删；服务不停机。
- 能力漂移 → 契约测试；发布版本与能力同源。
- 重复实例 → instance_id 幂等/租约；控制面按 last_seen 收敛。
- standalone 误注册 → 配置门禁；控制面缺席时退避重试，不影响请求路径。

## Migration Plan

greenfield：新增客户端与启动接线；无数据迁移；服务域配置/运行时保留。
