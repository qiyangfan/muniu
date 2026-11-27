# Proposal: 2026-11-26-add-muniu-service-registration-client（服务注册客户端（service-registration-client））

## Why

锦官 `service-registry` 需要 muniu 上报实例、能力、版本与健康；当前 muniu 只有分散的共享控制拉取，没有注册客户端，服务目录/健康/能力发现只有控制面半边。

## What Changes

- 新增服务身份注册/心跳客户端，上报 service_id/instance_id/version/capabilities/admin API 能力/权益键。
- managed 默认启用；standalone 可选禁用；控制面缺席不阻塞启动和核心流程。
- 幂等、退避重试、恢复补报；匿名拒绝；不上报凭据/连接串/服务域配置/服务只读 API endpoint；地址由锦官静态配置。

## Non-goals

- 不做服务发现/负载均衡。
- 不写共享控制数据。
- 不做控制面 UI。
- 不代理服务 admin API。

## Capabilities

### New Capabilities

- `service-registration-client`：服务注册、心跳与能力上报客户端

### Modified Capabilities

无

## Impact

启动/后台任务/服务身份配置；managed 注册、standalone 可选；不写共享控制数据；不上报服务域配置或凭据。
