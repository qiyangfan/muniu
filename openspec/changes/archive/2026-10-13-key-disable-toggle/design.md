# Design: key-disable-toggle

## D1 状态建模:独立 `disabled_at` 列,不扩 status CHECK

`api_keys.status` 现有 CHECK 只允许 `active|revoked`;SQLite 扩 CHECK 必须整表重建(建新表→拷数据→换名),对已有生产数据的风险与「加一个可逆态」的收益不成比例。选择独立可空列 `disabled_at`:

- `status='active' AND disabled_at IS NULL` = 启用(现状等价,存量行零迁移语义变化);
- `status='active' AND disabled_at IS NOT NULL` = 停用(可逆);
- `status='revoked'` = 终态,`disabled_at` 对其无意义(启用操作不得复活吊销 key)。

可逆运行态(停用)与终态(吊销)分列,判定顺序:先吊销后停用,互不遮蔽。

## D2 拒绝语义:401 auth_failed,message 可区分

停用与吊销/过期同属「身份态拒绝」,复用 `TypeAuthFailed`(错误类型表零膨胀);message 单列「已停用,请重新启用」(过期先例:用户动作不同——停用找管理员恢复,吊销找管理员重签)。实现走 errKeyDisabled 哨兵(Middleware 按哨兵选文案),与 errKeyExpired 同构。

## D3 管理形态:PATCH 布尔字段 + CLI 子命令

- PATCH `disabled: true|false`(省略=不动):与限额字段同 payload,复用写后 Invalidate 协议;对已吊销 key 设置 disabled 属无意义组合,允许落库但判定顺序保证吊销优先(不为不可能状态加校验分支)。
- CLI `keys disable/enable <prefix>`:按 prefix 操作与 revoke 对齐;CLI 与网关跨进程无法清鉴权缓存,输出如实提示收敛时长(吊销同款文案纪律,不承诺「立即生效」)。

## D4 控制台:徽章三态 + 启停按钮

状态徽章三态:启用(success 软底)/已停用(warning 软底)/已吊销(destructive 实底,复用既有 StatusBadge 变体);行操作增「停用/启用」(可逆操作,不属破坏性,不强制 AlertDialog;吊销既有确认保持不变)。
