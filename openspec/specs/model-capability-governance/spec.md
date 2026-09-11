# model-capability-governance Specification

## Purpose
让一份客户端参数在 model_routes 跨供应商换道时仍能被每个目标模型正确接受:通过配置声明的模型能力目录,在每次出站前按目标模型治理请求参数(drop 不支持项、clamp 越界项、执行思考锁),未声明则零干预透传——参数兼容是换道正确性的组成部分,而非客户端责任。

## Requirements

### Requirement: 能力约束仅来自显式声明,零内置
系统 SHALL 仅对配置中显式声明的模型能力约束执行参数治理;内置能力知识 MUST 为空。未被任何声明覆盖的「provider+model」或参数,系统 MUST 零干预、请求字节原样透传。能力声明违反格式或值域时,系统 MUST 在加载期拒绝启动(与 providers 校验同一纪律),不允许非法约束进入热路径。

#### Scenario: 未声明模型零干预
- **WHEN** 请求路由到未声明任何能力约束的 provider+model
- **THEN** 请求体与未引入本能力前逐字节一致,不产生任何治理日志与响应头标记

#### Scenario: 已声明模型仅干预声明覆盖的参数
- **WHEN** 某 provider+model 仅声明了不支持 temperature,请求还携带了 frequency_penalty
- **THEN** 仅 temperature 被移除,frequency_penalty 原样透传

#### Scenario: 非法能力声明拒绝启动
- **WHEN** 配置中某能力声明字段非法(如负数上限、参数名为空/含首尾空白/属协议结构字段)
- **THEN** 服务加载失败并报出指向具体条目的错误,不进入就绪状态

### Requirement: 治理点在每次换道尝试出站前,按目标模型执行且幂等
系统 SHALL 在每次 failover 尝试的上游请求组装前(协议形态映射之前)按「目标 provider+model」解析并执行能力治理;治理 MUST 是幂等纯函数——对同一请求重复治理结果一致,换道到新 provider+model 后 SHALL 按新目标重新治理。

#### Scenario: 换道后按新模型重新治理
- **WHEN** 首选模型声明输出上限 4096,请求 max_tokens=8192 被收敛为 4096 后该模型换道
- **THEN** 备选模型按其自身声明(如上限 8192)重新治理,不再沿用首选模型的收敛结果

#### Scenario: 治理幂等
- **WHEN** 同一请求对同一 provider+model 连续执行两次治理
- **THEN** 第二次治理不产生任何额外变更,请求结果与一次治理完全一致

### Requirement: drop 动作移除不支持参数
被声明为不支持的请求参数,系统 SHALL 在出站前完整移除,不得残留到上游请求。

#### Scenario: 黑名单参数被移除
- **WHEN** 某 provider+model 声明不支持 temperature,请求携带 temperature=0.7
- **THEN** 出站请求中不存在 temperature 字段

### Requirement: clamp 动作收敛数值越界
被声明了数值上限/区间的参数,系统 SHALL 把越界值收敛到声明边界;界内值 MUST 原样保留。

#### Scenario: 输出上限收敛
- **WHEN** 某 provider+model 声明输出上限 4096,请求 max_tokens=8192
- **THEN** 出站请求 max_tokens=4096

#### Scenario: 界内值不变
- **WHEN** 某 provider+model 声明输出上限 4096,请求 max_tokens=2048
- **THEN** 出站请求 max_tokens 仍为 2048,不产生干预记录

### Requirement: 思考锁按声明处置关闭思考的意图
对声明了「思考不可关」的模型,携带关闭思考意图的请求在默认(宽松)模式下 SHALL 归一为该模型允许的最小思考形态并留痕;strict 模式下 SHALL 返回 400,错误信封指明被拒参数。

#### Scenario: 宽松模式归一
- **WHEN** 某 provider+model 声明思考锁,请求显式要求关闭思考,strict 未开启
- **THEN** 出站请求按该模型允许的最小思考形态发出,响应含干预标记

#### Scenario: strict 模式拒绝
- **WHEN** strict 模式开启,同上请求到达
- **THEN** 返回 400,错误信封符合既有契约并指明思考锁约束

### Requirement: 一切干预必留痕
任何参数治理干预,系统 SHALL 写入结构化日志(provider、model、参数名、动作、原值);成功响应 MUST 携带客户端可见的响应头,列出被干预的参数。

#### Scenario: 响应头标注干预
- **WHEN** 请求被 drop 了 temperature 并 clamp 了 max_tokens 后成功返回
- **THEN** 响应头包含干预标记,列出 temperature 与 max_tokens 两项;日志可按 request_id 追溯两次干预明细

### Requirement: strict 模式把一切干预升级为拒绝
strict 模式开启时,任何本应发生的治理干预 SHALL 改为返回 400,错误信封符合项目统一错误契约,指明模型、参数与约束原因;未发生干预的请求 MUST 不受影响。

#### Scenario: strict 下可治理项被拒
- **WHEN** strict 开启,请求对声明了不支持 temperature 的模型携带 temperature
- **THEN** 返回 400,错误信封指明该模型不支持 temperature;未携带 temperature 的同模型请求正常放行
