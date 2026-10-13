# Proposal: key-disable-toggle

## Why

key 生命周期目前只有单向终点:吊销不可逆。真实运维的高频动作「疑似泄露先暂停、查完再恢复」现在只能吊销重签(丢配置、丢列表历史),或借 PATCH 置过去时刻 `expires_at` 表达停用(恢复=清过期,但本有过期设定的 key 会被一并抹掉,语义错位)。作品集完整度视角,key 生命周期叙事(签发→启停→限额→过期→吊销)缺「启停」一环,主流网关(one-api 禁用 / LiteLLM block)均为标配。

## What Changes

- 迁移 0009:`api_keys` 增 `disabled_at TEXT`(NULL=启用)。可逆禁用不并入 `status` CHECK(SQLite 改 CHECK 需整表重建,风险与收益不成比例);吊销仍是唯一终态,禁用不改变 revoked 语义。
- 鉴权:查库路径拒绝已停用 key,401 `auth_failed`,message 与无效/吊销/过期可区分;写后复用 Invalidate 协议,下一请求即生效。
- 管理面:`PATCH /admin/v1/keys/{id}` 增 `disabled: true|false`(省略=不动);列表回显停用状态。
- CLI:`muniu keys disable <prefix>` / `muniu keys enable <prefix>`(CLI 与网关跨进程,如实提示缓存收敛时长,与 revoke 同款文案纪律)。
- 控制台:密钥页状态徽章区分「已停用」+ 启停操作。

## Capabilities

### Added Capabilities

(无新能力域——启停并入既有 `api-key-lifecycle` 主规格,以 delta ADDED Requirement 落地。)
