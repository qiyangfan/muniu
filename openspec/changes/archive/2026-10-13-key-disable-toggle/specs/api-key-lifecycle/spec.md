# key-disable-toggle 变更(api-key-lifecycle)

## ADDED Requirements

### Requirement: 运行态启停(可逆禁用)

key 必须 (MUST) 支持可逆停用与恢复:管理面经 `PATCH /admin/v1/keys/{id}` 的 `disabled` 字段(true=停用/false=恢复,省略=不动),CLI 经 `keys disable <prefix>` / `keys enable <prefix>`。停用 key 的数据面请求必须 (MUST) 被拒绝:401 `auth_failed`,message 与无效/吊销/过期可区分(含「已停用」语义)。停用/恢复写后必须 (MUST) 立即失效鉴权缓存(下一请求即生效,不等缓存 TTL)。停用必须 (MUST) NOT 改变吊销终态语义:已吊销 key 不得经恢复操作复活。key 列表(管理 API 与控制台)必须 (MUST) 回显停用状态。

#### Scenario: 停用即时生效(含缓存命中路径)

- **WHEN** 停用一把此前 10s 内已被鉴权缓存命中过的 key,随后以该 key 发起请求
- **THEN** 返回 401 `auth_failed`,message 含「已停用」语义,不等缓存 TTL

#### Scenario: 恢复放行且其余字段无损

- **WHEN** 对已停用 key 执行恢复(PATCH `disabled=false` / CLI enable)
- **THEN** 请求恢复放行,该 key 的限额、过期等其余字段保持原值不变

#### Scenario: 吊销终态不可复活

- **WHEN** 对已吊销 key 执行恢复操作
- **THEN** 该 key 仍被 401 拒绝(吊销语义),不得恢复为可用

#### Scenario: 缺省零回归

- **WHEN** PATCH 请求不携带 `disabled` 字段
- **THEN** 行为与本变更前逐字节一致(既有测试零改动通过)

#### Scenario: 列表回显停用状态

- **WHEN** 停用一把 key 后查询 `GET /admin/v1/keys` 与控制台密钥页
- **THEN** 该条目如实展示停用状态,与启用/吊销可区分
