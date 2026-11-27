# Spec Delta

## Purpose

muniu 的运行时命令执行器：以服务身份消费锦官跨服务命令队列中的缓存清理等有限命令，幂等执行并回执；本地运行命令独立执行，不进入队列；不接受任意配置或 SQL。

## ADDED Requirements

### Requirement: 命令执行与回执

muniu SHALL 以服务身份消费锦官命令队列，执行有限动作并回执；命令 SHALL 带幂等键、action/params schema/version；服务 SHALL 按 X-01 schema 校验，未知 action/版本或参数不合法 SHALL 拒绝并 ack failed；重复 SHALL 只执行一次；失败 SHALL 回执 failed；SHALL NOT 接受任意配置/SQL；本地运行命令 SHALL 由内嵌控制台直接执行，锦官不可用仍可用。

#### Scenario: 清缓存

- WHEN claim cache_purge
- THEN 执行并 ack

#### Scenario: 本地命令

- GIVEN 锦官不可用
- WHEN 在内嵌控制台清缓存
- THEN 本地执行并审计

#### Scenario: 拒绝任意配置

- WHEN 收到配置写入命令
- THEN 拒绝，ack failed

#### Scenario: 未知 action/参数

- WHEN 收到未注册 action/版本或参数不合法
- THEN 拒绝，ack failed，不执行

#### Scenario: 匿名 claim

- WHEN 无服务身份 claim
- THEN 401/403，不执行
