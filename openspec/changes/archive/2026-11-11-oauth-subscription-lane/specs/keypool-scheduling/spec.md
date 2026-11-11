# Spec Delta

## ADDED Requirements

### Requirement: 账号凭据来源

keys 的账号引用成员(`account:`)必须 (MUST) 在池快照解析时从账号存储取回凭据并按普通 key 成员入池;解析失败(账号不存在/已删除/凭据空)按该 key 不可用收缩,不得 (MUST NOT) 使请求携带空凭据出站,也不得 (MUST NOT) 因账号凭据引入任何调度特例分支——WRR/冷却/三态/欠费分桶/熔断让位对账号凭据与静态 key 逐字节同一语义。凭据刷新成功后,池内后续请求必须 (MUST) 取到新 token(零重启、零拓扑操作)。

#### Scenario: 解析失败收缩
- **WHEN** keys 含 `account: acc_x` 且账号存储中无此账号(运行中被删)
- **THEN** 该渠道池收缩该成员(其余 key 正常调度),留痕可观测

#### Scenario: 启动装配时引用不存在账号失败
- **WHEN** 配置文件源声明 `account: acc_x` 而账号存储无此账号,进程启动装配
- **THEN** 装配失败(fail-fast);控制面拓扑同场景下快照替换被拒绝、旧快照保留

#### Scenario: 调度形态等价
- **WHEN** 全账号渠道与全静态 key 渠道各按同权重同状态运行
- **THEN** 两侧 Pick/Report 行为逐字节一致,账号成员无任何调度特例

#### Scenario: 刷新后凭据生效
- **WHEN** 账号 token 刷新成功且池已在调度中
- **THEN** 下一请求出站即携带新 token,无需重启或热替换
