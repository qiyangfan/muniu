# Spec Delta

## ADDED Requirements

### Requirement: 会话身份解析与归属隔离

会话身份必须 (MUST) 按序解析:请求头 `X-Session-Id`(可选)> `apiKeyID + 首条 user 消息文本的 SHA-256`;亲和条目必须 (MUST) 绑定签发 key 归属(apiKeyID),跨 key 不可见;messages 无 user 角色消息的请求必须 (MUST) 不参与亲和(不查不写)。

#### Scenario: 自报头优先

- **WHEN** 同一 apiKey 的请求带 `X-Session-Id` 且同一会话后续轮次仍带同值
- **THEN** 命中同一条目,消息哈希不参与判定

#### Scenario: 消息哈希兜底

- **WHEN** 请求无自报头
- **THEN** 以 apiKeyID+首条 user 消息哈希为身份;同会话后续轮次首条 user 消息不变则同身份(中途插入系统提示/工具提醒噪声不改变身份)

#### Scenario: 跨 key 不可见

- **WHEN** 另一 apiKey 发送相同 `X-Session-Id` 或相同首条 user 消息
- **THEN** 不命中前一条目(归属隔离),不产生跨 key 影响

#### Scenario: 无 user 消息不亲和

- **WHEN** messages 数组不含 user 角色消息
- **THEN** 直走既有路由,亲和表零读写

### Requirement: worth 闸门——有缓存证据才粘

仅当某 (渠道, key) 上次成功 attempt 的 usage 上报**缓存读** token >0 时,才允许 (MUST) 建立会话对其的粘性(写侧/缓存建立的各方言归一不一,v1 不参与判定——design 登记);无缓存证据必须 (MUST) 不粘。

#### Scenario: 有缓存读建立粘性

- **WHEN** 某会话请求经 (渠道A, key1) 成功且 usage 缓存读 >0
- **THEN** 亲和表记录该会话 → (渠道A, key1),TTL 10 分钟

#### Scenario: 无缓存证据不粘

- **WHEN** 某 attempt 成功但缓存读 =0
- **THEN** 不建立粘性,后续轮次按既有 smooth WRR 路由

### Requirement: 健康前提下的重排与失败即清

候选循环前,stick 存在且未过期且 stick 候选健康(未冷却/未禁用/未欠费、渠道未熔断)时必须 (MUST) 置于候选序最前且池选择同 key 优先;stick 候选因池冷却/渠道熔断暂不可用时必须 (MUST) 按原序路由且条目保留(候选恢复后复粘);本次尝试在 stick 候选上失败或发生换道时必须 (MUST) 立即清除该条目;池冷却/渠道熔断必须 (MUST) 恒优先于亲和;亲和必须 (MUST) 不引入任何额外重试;语义缓存命中路径必须 (MUST) 零读写亲和。

#### Scenario: 健康 stick 重排

- **WHEN** 会话第 N 轮,stick=(渠道A, key1) 未过期且健康
- **THEN** 候选序渠道A 最前且池优先选中 key1,上游前缀缓存得以延续

#### Scenario: 冷却让位但保留条目

- **WHEN** stick 候选 key1 欠费冷却中
- **THEN** 按原候选序路由;条目保留,key1 恢复后自动复粘

#### Scenario: 失败即清

- **WHEN** stick 候选本次尝试失败并换道成功
- **THEN** 立即清除该会话条目;本轮按换道语义完成,后续轮次重新按 worth 闸门建立粘性

#### Scenario: 熔断恒优先

- **WHEN** stick 渠道处于健康熔断降级态
- **THEN** 该渠道整路跳过(既有熔断语义);熔断跳过本身不产生额外计数,转原序服务成功按 stale 计(stale 定义:条目过期或 stick 候选暂不可用)

#### Scenario: 语义缓存命中不经过亲和

- **WHEN** 会话请求命中语义缓存
- **THEN** 直接回放缓存响应,亲和表零读写

### Requirement: 可观测与容量纪律

亲和结果必须 (MUST) 以 `muniu_cache_affinity_total{result=hit|miss|stale}` 计数暴露(hit=粘住;miss=无条目;stale=条目过期或 stick 候选暂不可用转原序);亲和表必须 (MUST) 有容量上限(LRU 淘汰)与 TTL(缺省 10 分钟);亲和表必须 (MUST) 为进程内存态,不落盘(重启归零可接受,重启后按 worth 闸门自然重建)。

#### Scenario: 计数三分

- **WHEN** 会话请求走完亲和路径且未命中语义缓存、未触发熔断跳过
- **THEN** hit/miss/stale 三者之一计数递增,且仅其一

#### Scenario: 容量淘汰

- **WHEN** 条目数达上限后写入新条目
- **THEN** 按 LRU 淘汰最旧条目,零 panic、零阻塞
