# Spec Delta

## Purpose

木牛服务管理台：muniu 内嵌的服务域配置 + 运行时管理界面；managed 共享控制只读；standalone 保留本地配置；服务域配置写只在本地，锦官只读展示服务只读 API/投影。

## ADDED Requirements

### Requirement: 木牛服务管理台

木牛控制台 SHALL 提供服务域配置页（key 签发/池、供应商凭据、OAuth、插件、余额、缓存）与运行时页（健康/日志/命令/投影）；managed 下共享控制（租户/价目/拓扑/配额/共享策略）SHALL 只读；standalone SHALL 保留本地配置；服务域配置写入 SHALL 经服务 admin API 本地完成，锦官不得代理写。

#### Scenario: standalone 诊断

- WHEN 运维打开内嵌控制台
- THEN 看到服务域配置、运行时页面与只读共享控制

#### Scenario: managed 共享控制只读

- WHEN 尝试改价目/租户
- THEN 无入口；服务端拒绝

#### Scenario: 服务域配置可写

- WHEN ADMIN 签发 key/改供应商凭据
- THEN 允许，服务落库并审计

#### Scenario: 管理域只读

- WHEN 锦官读取服务域配置
- THEN 通过服务只读 API/投影展示，不写、不落库

### Requirement: 管理域只读 API

服务 SHALL 提供只读 API/投影供管理域读取服务域配置与运行时摘要；SHALL 认证服务身份/管理员只读；SHALL NOT 返回凭据/token/密钥；SHALL NOT 提供写入口；服务宕机时管理域读 last-known 并标 stale。

#### Scenario: 只读读取

- WHEN 管理域以只读身份请求
- THEN 返回脱敏投影，带 observed_at/stale

#### Scenario: 敏感值不返回

- WHEN 读取含凭据配置
- THEN 脱敏，不返回原值

#### Scenario: 写拒绝

- WHEN 管理域尝试经只读 API 写
- THEN 无入口/405/403

### Requirement: 命令

控制台 SHALL 支持本地运行命令（清缓存/重载）直接执行并审计；跨服务命令 SHALL 显示锦官 pending/acked/failed；锦官不可用 SHALL 不阻断本地命令。

#### Scenario: 本地命令

- WHEN 锦官不可用
- THEN 本地清缓存仍可执行

#### Scenario: 跨服务命令

- WHEN 提交锦官命令
- THEN 显示 pending/acked/failed
