# break-glass Specification

## Purpose

木牛的运行时 break-glass：独立 CLI 直连运行时存储，用于网关或控制面不可用时的受控数据修复，带独立凭证、原因、二次确认、审计与限时，不嵌入控制台。

## Requirements

### Requirement: 独立 break-glass 通道

break-glass SHALL 为独立 CLI/工具，使用独立凭证直连运行时存储；SHALL 要求原因、二次确认、限时与审计；SHALL NOT 嵌入控制台；SHALL NOT 作为日常写路径。

#### Scenario: 应急修复

- GIVEN 网关不可用且需修复运行时数据
- WHEN 运维使用独立凭证执行
- THEN 操作记录原因/摘要/影响行数

#### Scenario: 无凭证拒绝

- WHEN 未提供独立凭证
- THEN 拒绝，不连接存储

### Requirement: 修复边界与审计

break-glass 操作 SHALL 限于文档化修复场景；每次操作 SHALL 审计 actor、原因、SQL 摘要、影响行数与时间；SHALL NOT 暴露给普通管理员；SHALL NOT 绕过备份/回滚要求。

#### Scenario: 审计留痕

- WHEN 执行修复
- THEN 审计有 actor/原因/摘要/行数

#### Scenario: 控制台不可用

- WHEN 查找 break-glass 入口
- THEN 不存在/无权
