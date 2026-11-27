# Spec Delta

## Purpose

木牛的服务注册客户端：managed 下以服务身份向锦官注册实例、能力/权益键、版本与 admin API 能力并周期心跳；standalone 可选禁用；控制面缺席不阻塞数据面，不上报凭据或服务域配置。

## ADDED Requirements

### Requirement: 服务注册与心跳

muniu SHALL 在 managed 模式以服务身份向锦官注册 service_id/instance_id/version/capabilities/admin API 能力/权益键并周期心跳；standalone MAY 禁用注册；心跳 SHALL 作为健康信号，SHALL NOT 上报伪造 ready/健康；注册/心跳 SHALL 幂等并可重试；匿名或未认证 SHALL 拒绝；锦官不可用 SHALL NOT 阻塞启动或数据面请求，SHALL 退避重试并在恢复后补报；SHALL NOT 上报凭据、服务域配置或服务只读 API endpoint。

#### Scenario: managed 注册成功

- WHEN muniu 以 managed 模式和服务身份启动
- THEN 锦官可见实例/能力/版本，last_seen 更新

#### Scenario: standalone 禁用

- GIVEN muniu 以 standalone 启动且注册关闭
- WHEN 启动完成
- THEN 不注册且不报错，数据面正常

#### Scenario: 控制面缺席

- GIVEN 锦官不可用
- WHEN muniu 启动
- THEN 数据面可用，后台退避重试

#### Scenario: 匿名注册

- WHEN 无服务身份注册/心跳
- THEN 401/403，不落库

#### Scenario: 不泄露

- WHEN 检查注册 payload
- THEN 无凭据/连接串/服务域配置/endpoint 字段

### Requirement: 能力声明与版本

muniu SHALL 从代码/配置单一来源生成能力声明与版本；能力/权益键 SHALL 与锦官 service-registry 契约一致；能力变更 SHALL 在下一次注册/心跳可见；SHALL NOT 用注册伪造健康或版本。

#### Scenario: 能力变更

- WHEN 发布新能力并心跳
- THEN 锦官显示新能力/版本

#### Scenario: 契约漂移

- WHEN 能力键与 service-registry 契约不一致
- THEN 跨仓契约测试失败，阻止发布

#### Scenario: 不伪造健康

- WHEN 数据面未就绪
- THEN 不上报 ready/健康为正常
