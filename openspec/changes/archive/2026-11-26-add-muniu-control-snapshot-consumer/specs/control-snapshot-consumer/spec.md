# Spec Delta

## Purpose

muniu 的共享控制快照消费者：以服务身份从锦官拉取并验签价目、拓扑、吊销、租户/配额/共享策略包快照，本地缓存 last-known 并按版本收敛；standalone 不依赖锦官启动。

## ADDED Requirements

### Requirement: 控制快照消费与应用

muniu SHALL 以服务身份拉取并验签控制快照，原子应用价目/拓扑/吊销/租户配额/共享策略包；快照 SHALL 带版本/ETag/observed_at；旧版本 SHALL NOT 覆盖新版本；应用失败 SHALL 保留旧目录；managed 下 SHALL 关闭本地价目/拓扑/租户写入口，standalone 保留本地配置。

#### Scenario: 应用新快照

- WHEN 锦官返回签名有效的新版本
- THEN 价目/拓扑/吊销/配额原子替换

#### Scenario: 应用失败

- GIVEN 快照非法/签名失败
- WHEN 应用
- THEN 保留旧目录，告警

#### Scenario: managed 第二写者

- WHEN 尝试本地改价目/拓扑/租户
- THEN 拒绝/只读；standalone 模式除外

### Requirement: last-known 与 fail-closed

muniu SHALL 缓存 last-known 快照；锦官不可用 SHALL NOT 阻止启动；安全类（吊销/共享策略/租户/权益）缺失或非法 SHALL fail-closed，非安全类 SHALL last-known 并标 stale。

#### Scenario: 控制面缺席

- GIVEN 锦官不可用
- WHEN muniu 启动
- THEN 用 last-known 启动

#### Scenario: 吊销缺失

- GIVEN 无有效吊销快照
- WHEN 鉴权
- THEN 拒绝/保守处理，不 fail-open
