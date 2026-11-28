# Spec Delta

## ADDED Requirements

### Requirement: 冷却桶独立计时

key 必须 (MUST) 为限频(429)、欠费、5xx 瞬态维护互不覆盖的独立冷却截止;任一桶未到期即不可选;新事件只更新所属桶,不得 (MUST NOT) 改写其他桶的至时刻或阶梯;成功只清零连续计数与已到期桶,不得 (MUST NOT) 取消仍在计时的其他桶。

#### Scenario: 在途 429 不覆盖欠费桶

- **WHEN** key 处于欠费冷却,一笔在途请求随后收到 429
- **THEN** rate 桶按 429 语义计时,credit 桶保持原至时刻与阶梯;充值后 credit 可正常提前释放

#### Scenario: 两桶并存

- **WHEN** 同一 key 同时存在未到期的 rate 与 credit 冷却
- **THEN** Pick 不选中;两个桶各自到期,不因一个桶到期而提前放行

#### Scenario: 成功不清其他桶

- **WHEN** key 的一次真实请求成功,同时仍有未到期的限频冷却
- **THEN** 成功清零连续计数,但不取消限频冷却

### Requirement: 桶级持久化与释放

长冷却必须 (MUST) 按 `(provider, key, bucket)` 多行持久化并各自恢复;credit 桶全程落库,rate 桶 ≥5 分钟落库,5xx 瞬态不落库;余额充值回写只允许 (MUST) 清除 credit 桶,rate 桶不受影响;清除事件必须 (MUST) 只删对应桶行;成功与启动加载必须 (MUST) 清除已到期行,不得 (MUST NOT) 提前删除未到期行。

#### Scenario: 充值只释放欠费

- **WHEN** key 同时有 credit 与 rate 冷却,余额回写为正值
- **THEN** 只清除 credit 行并清零欠费阶梯;rate 冷却继续生效

#### Scenario: 重启恢复双桶

- **WHEN** key 的 credit 与 rate 长冷却均已落库,网关重启
- **THEN** 两个桶的至时刻与阶梯分别恢复,任一到期末到前 key 不可选

#### Scenario: 瞬态不落库

- **WHEN** key 因 5xx 连续失败进入 30 秒瞬态冷却
- **THEN** 不产生持久化行

#### Scenario: 过期行清理

- **WHEN** 库内冷却行已过期,进程重启或该 key 随后成功
- **THEN** 过期行被删除,未到期桶行保持不变
