# traffic-governance Specification

## Purpose
TBD - created by archiving change cluster-ratelimit-shared-view. Update Purpose after archive.

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
