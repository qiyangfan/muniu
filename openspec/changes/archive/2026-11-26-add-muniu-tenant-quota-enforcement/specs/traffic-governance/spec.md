# Spec Delta

## ADDED Requirements

### Requirement: 租户级配额执行

muniu SHALL 按锦官快照执行租户级 RPM/TPM/日配额；请求 SHALL 同时满足租户与 key 配额，任一超限 SHALL 拒绝；0=不限；快照缺失/非法 SHALL fail-closed；managed 下租户/配额本地只读，standalone 保留本地配置。

#### Scenario: 租户上限

- GIVEN 租户配额耗尽，key 充足
- WHEN 请求
- THEN 429，key 不掩盖租户上限

#### Scenario: key 上限

- GIVEN 租户充足，key RPM 耗尽
- WHEN 该 key 请求
- THEN 429
