## MODIFIED Requirements

### Requirement: 倍率缩放纪律

倍率必须 (MUST) 以基点(万分之一)表示并作用于整张价目卡(三段价统一缩放);窗口有效价必须 (MUST) 为 ceil(缺省价 × 倍率)——向上取整保守计价,窗口价永不低估;溢出必须 (MUST) 报错;「cached_input 缺省同 input」的语义必须 (MUST) 在窗口内保持(0 值缩放仍为 0,由计价阶段统一解析)。

#### Scenario: 保守取整
- **WHEN** 缺省输入价 3 credit、倍率 0.5
- **THEN** 窗口输入价为 2 credit(向上取整)

#### Scenario: 缓存价缺省语义保持
- **WHEN** 缺省 cached_input 未配置(0=同 input)且窗口倍率 0.5
- **THEN** 窗口 cached_input 仍为 0,由计价阶段统一按「同 input(已缩放)」解析

### Requirement: 计价选窗时机

计量行落账必须 (MUST) 按请求完成时刻选窗,行内价目即该窗有效价,对账可逐行复核;窗口换算必须 (MUST) 发生在加载期,不得进入请求热路径。

#### Scenario: 端到端倍率落账
- **WHEN** 配置全天半价窗且上游返回 usage prompt=7 / completion=9(缺省价 input 2 credit/tok、output 8 credit/tok)
- **THEN** 计量行 cost_credits = 43(缺省价为 86)
