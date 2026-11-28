# traffic-governance Specification

## Purpose

key 级 RPM/TPM/日配额的计数执行契约:计数后端可插拔(缺省进程内 `inmem`,cluster 形态可选 `redis` 共享视图),且后端选择不得改变限额判定语义;redis 不可用时按降级纪律退回进程内计数并持续告警,既不 fail-open 也不阻塞热路径(ADR-0008)。

## Requirements

### Requirement: 限流计数后端可插拔

key 级 RPM/TPM/日配额的计数必须 (MUST) 经可插拔后端执行:缺省 `inmem`(进程内,standalone 缺省语义与历史行为逐字节等价);cluster 配置可选 `redis`(原子 INCR + TTL 窗口,N 副本共享同一计数视图)。后端切换不得 (MUST NOT) 改变限额判定语义(阈值含义、窗口语义、先到先得次序)。

#### Scenario: standalone 零感知

- **WHEN** 配置未含任何限流后端段(缺省)
- **THEN** 行为与既有 inmem 实现完全一致,不引入任何网络依赖

#### Scenario: cluster 共享视图

- **WHEN** 3 副本以 redis 后端服务同一 key(RPM=60)
- **THEN** 整站一分钟内对该 key 的放行总数 ≤ 60(共享计数,×N 消除)

### Requirement: redis 后端降级纪律

redis 后端在连接失败或超时时必须 (MUST) 降级回进程内计数并持续告警,禁止 (MUST NOT) 因后端不可用而放行所有限流判定(fail-open)或阻塞请求热路径;恢复后自动回到共享视图。

#### Scenario: redis 不可达

- **WHEN** redis 超时且请求到达
- **THEN** 该请求以进程内计数判定后放行/拒绝,日志告警;请求延迟不因 redis 故障显著上升

#### Scenario: 恢复收敛

- **WHEN** redis 恢复可达
- **THEN** 新计数回到共享视图;降级期实例本地计数不合并(短窗口计数误差可接受,日配额由控制面 ingest 聚合兜底)

### Requirement: 后端行为等价

redis 与 inmem 后端必须 (MUST) 通过同一限流语义测试集对拍:任一请求序列下两后端的判定结果(放行/拒绝/剩余额度)逐例一致;对拍集冻结入库,后端实现变更须回放。

#### Scenario: 对拍回放

- **WHEN** 对冻结的语义测试集分别以两后端回放
- **THEN** 判定序列 diff 为空

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

### Requirement: 租户在途并发准入

每个租户必须 (MUST) 受跨 lane 的在途并发上限约束:同一租户的 chat/messages/responses/gemini/embeddings/images/multimodal 上游调用共享一个在途计数;达到上限时新请求必须 (MUST) 在上游调用前返回 429 `tenant_inflight_exceeded`,不得 (MUST NOT) 触上游或落 usage 行;槽位必须在所有出口释放(成功/失败/断连/预算中断);上限 0=不限。

#### Scenario: 单租户占满不影响他人

- **WHEN** 租户 A 达到在途上限,租户 B 发起请求
- **THEN** A 新请求 429 `tenant_inflight_exceeded`,B 正常服务

#### Scenario: 槽位释放后恢复

- **WHEN** A 的在途请求完成或断连,在途数降到上限以下
- **THEN** A 的下一请求正常准入

#### Scenario: 断连释放

- **WHEN** A 的长流式请求客户端断连
- **THEN** 槽位立即释放,不遗留占位

#### Scenario: 默认不启用

- **WHEN** 未配置任何租户在途上限
- **THEN** 准入恒放行,行为与本变更前一致

### Requirement: 租户并发配置与观测

租户在途上限必须 (MUST) 支持 `tenant_default_max_inflight` 全局缺省(0=不限)与 `tenant_max_inflight` 覆盖表;managed 租户必须 (MUST) 消费快照 `entitlements.quotas` 的 `max_inflight` 维度(0=不限),维度缺席时必须 (MUST) 回落本地缺省;网关必须 (MUST) 暴露 `muniu_tenant_inflight` 在途水位与 `muniu_tenant_admission_denials_total{tenant}` 拒绝计数指标,并在拒绝日志中携带 tenant 与 request_id。

#### Scenario: managed 使用本地缺省

- **WHEN** managed 租户快照不含 `max_inflight` 维度,本地缺省为 8
- **THEN** 该租户按 8 执行准入

#### Scenario: managed 配额覆盖

- **WHEN** 快照下发 `max_inflight=3`,本地缺省为 8
- **THEN** 该租户按 3 执行准入;后续快照移除该维度后恢复按 8

#### Scenario: 拒绝可观测

- **WHEN** 租户因在途超限被拒绝
- **THEN** `muniu_tenant_admission_denials_total{tenant}` 递增,`muniu_tenant_inflight{tenant}` 反映在途数,日志含 tenant 与 request_id
