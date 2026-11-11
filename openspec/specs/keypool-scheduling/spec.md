# keypool-scheduling Specification

## Purpose
keypool 多 key 调度的欠费分桶与持久化纪律:上游欠费证据(封闭白名单)独立于限流桶按阶梯冷却,冷却状态跨重启/热替换无损;余额观测逐 key 回写联动调度(欠费让位、充值提前释放),观测与调度闭环。失败分桶、按因休息、休息可被证据提前赦免,是「知道没钱」到「不再往那儿发」的完整链路。

## Requirements

### Requirement: 欠费桶与限流桶分离

上游欠费证据必须 (MUST) 使 key 进入**欠费冷却**而非通用 429 冷却:阶梯 30 分钟起步、连续欠费 ×2 推进、2 小时封顶;任何一次成功必须 (MUST) 清零阶梯;欠费冷却期间 Pick 必须 (MUST) 不选中该 key。failover 错误分类与判定表必须 (MUST) 零改动(欠费对换道语义仍归限流类,首 token 前照常换道)。

#### Scenario: 402 进欠费桶

- **WHEN** 某上游调用返回 HTTP 402
- **THEN** 该 key 记欠费冷却 30 分钟(非 429 桶的 Retry-After 语义),后续 Pick 不选中

#### Scenario: 阶梯推进与封顶

- **WHEN** 同一 key 连续第三次欠费
- **THEN** 欠费冷却时长为 2 小时(30m→1h→2h 封顶),不再增长

#### Scenario: 成功清零阶梯

- **WHEN** 冷却到期后该 key 的一次调用成功
- **THEN** 欠费阶梯清零,后续欠费重新从 30 分钟起步

#### Scenario: insufficient_quota 进欠费桶

- **WHEN** OpenAI 兼容上游返回错误体错误码 `insufficient_quota`
- **THEN** 该 key 进欠费桶;同状态码的非白名单形态仍走既有桶

### Requirement: 欠费证据白名单封闭

欠费证据识别必须 (MUST) 限于封闭清单(HTTP 402;错误体错误码 `insufficient_quota`),必须 (MUST) 不按错误文案做模糊匹配;白名单外的一切失败必须 (MUST) 维持既有桶与分类。

#### Scenario: 文案不触发欠费桶

- **WHEN** 上游 400 错误文案含「余额不足」字样但状态码非 402 且错误码不在白名单
- **THEN** 按既有 bad_request 语义处理,不进欠费桶

#### Scenario: 白名单外 429 维持限流桶

- **WHEN** 上游 429 无欠费错误码且带 Retry-After
- **THEN** 走既有 429 桶(Retry-After 语义),不进欠费桶

### Requirement: 冷却持久化与热替换无损

冷却时长 ≥5 分钟的状态(欠费桶与 429 桶长冷却)必须 (MUST) 落库(渠道/键/桶类型/至时刻/连续步)并在启动时加载恢复;<5 分钟瞬态冷却必须 (MUST) 不落库;SIGHUP 拓扑热替换必须 (MUST) 保留运行态冷却(键仍在配置则状态无损,键已移除则对应持久行清除);重启后对已冷却 key 必须 (MUST NOT) 立即重试。

#### Scenario: 重启恢复长冷却

- **WHEN** 某 key 欠费冷却至 T+2h,网关在 T+30min 重启
- **THEN** 启动加载后该 key 在 T+2h 前不被 Pick 选中

#### Scenario: 瞬态冷却不落库

- **WHEN** 某 key 因 5xx 连续失败冷却 30 秒
- **THEN** 不产生落库写入

#### Scenario: SIGHUP 保留运行态

- **WHEN** 某 key 欠费冷却中执行 SIGHUP 拓扑热替换且该 key 仍在配置
- **THEN** 冷却至时刻与阶梯无损;该 key 已从配置移除则其持久行被清除

### Requirement: 余额回写联动调度

逐 key 余额源(deepseek/moonshot/openrouter/billing-compat)的有效余额=0 必须 (MUST) 触发该 key 记欠费冷却(阶梯与上游欠费桶共用);后续评估该 key 余额>0 必须 (MUST) 提前解除欠费冷却并清零阶梯;账户级源(aliyun-bss/volc-bss/manual)必须 (MUST) 不回写池;回写必须 (MUST) 幂等——重复评估不推进阶梯、不刷新至时刻。

#### Scenario: 余额归零回写

- **WHEN** deepseek 余额源评估某 key 有效余额=0
- **THEN** 该 key 记欠费冷却 30 分钟,metrics cause=balance

#### Scenario: 充值提前释放

- **WHEN** 该 key 欠费冷却中且下一轮评估余额>0
- **THEN** 立即解除冷却并清零阶梯,下一轮 Pick 可选中

#### Scenario: 账户级源不回写

- **WHEN** aliyun-bss 源评估账户余额=0
- **THEN** 仅触发既有告警链,池状态零变化

#### Scenario: 回写幂等

- **WHEN** 同一 key 连续两轮评估有效余额均=0
- **THEN** 冷却至时刻与阶梯不因重复评估而推进

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
