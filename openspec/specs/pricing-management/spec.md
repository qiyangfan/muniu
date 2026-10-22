# pricing-management Specification

## Purpose
运行态价目能力:厂商改价是运维动作,通过管理面在线覆盖价目(持久化 + 原子热替换),即时生效零重启;YAML 静态兜底保证独立部署开箱可用。

## Requirements

### Requirement: 价目来源与回落语义

系统必须 (MUST) 按「DB 运行态覆盖 > YAML 静态兜底」解析每个模型别名的有效价目;DB 行必须 (MUST) 整体取代该别名(含时段窗),无 DB 行的别名回落 YAML 缺省价。

#### Scenario: DB 覆盖生效
- **WHEN** 别名 X 存在运行态覆盖且其缺省输入价为 0.22
- **THEN** 转发计量对该别名的计价使用 0.22,与 YAML 配置无关

#### Scenario: 撤销覆盖回落 YAML
- **WHEN** 删除别名 X 的运行态覆盖
- **THEN** 有效价目立即回落为 YAML 中 X 的缺省价(或无价目则成本记 0)

#### Scenario: YAML 行来源标注
- **WHEN** 查询价目列表
- **THEN** 每条目标注来源 `db`(运行态覆盖,含时段窗与更新时间)或 `yaml`(静态兜底,仅缺省价)

### Requirement: 在线改价即时生效

管理面写入价目必须 (MUST) 先校验后持久化,再原子替换转发侧价目目录;改价后下一请求必须 (MUST) 使用新价,全程不得要求重启。

#### Scenario: 改价下一请求生效
- **WHEN** 管理面成功写入别名 X 的新价目
- **THEN** 后续任一转发请求的计量行即按新价计费,且服务全程可用(无重启窗口)

#### Scenario: 校验失败整单拒绝
- **WHEN** 写入请求含非法字段(币种非 CNY/USD、价目非十进制 ≤9 位小数、倍率超 (0,10] 或超 4 位小数、时段窗格式非法)
- **THEN** 整单拒绝返回 400,不落库、不刷新、目录保持原状

#### Scenario: 脏数据不进热路径
- **WHEN** 目录重建遇到任何无法构造的存储行
- **THEN** 重建整体失败并保持当前目录不变(宁可维持旧价,不放行脏数据),失败可观测(日志告警)

### Requirement: 价目校验纪律

价目与倍率必须 (MUST) 全程零浮点:人类可读十进制解析(价目 ≤9 位小数、倍率 ≤4 位小数且取值 (0,10]),加载期一次性换算为整数(credit,1 credit = 本位币 1e-9),运行时必须 (MUST) 为纯整数运算。

#### Scenario: 人类可读往返
- **WHEN** 对任一合法价目字串(如 "0.44")与倍率字串(如 "0.5")做「解析 → 格式化」
- **THEN** 输出与输入逐字符相等(规范化形态恒等往返)

### Requirement: 价目管理 API 契约

管理 API 必须 (MUST) 提供 GET 列表(按别名升序)、PUT 幂等覆盖、DELETE 撤销运行态覆盖(无覆盖返回 404);必须 (MUST) 经管理 token 鉴权;内部错误必须 (MUST) 收敛为归一信封,不得外泄驱动细节。

#### Scenario: 全流程
- **WHEN** 依次执行 GET → PUT → GET → DELETE → DELETE
- **THEN** 分别得到:yaml 来源条目 → 200 ok → db 来源条目与新价 → 200 ok → 404

### Requirement: per-image 价目维度与回落语义

价目管理域必须 (MUST) 增设 per-image 价目形态(与 token 价目平行的第二维度):键空间同为模型别名,值形态为「币种 + 兜底单价 + size|quality 档位表」。来源与回落必须 (MUST) 与 token 价目同款——DB 运行态覆盖整体取代该别名,YAML 静态兜底开箱可用,列表标注来源。价目数值必须 (MUST) 全程零浮点(人类可读十进制 ≤9 位小数,加载期换算整数 credit);档位表必须 (MUST) 以 JSON 存储、读取期严格解析,解析失败整目录重建拒绝(宁拒不放行)。token 价目与 per-image 价目必须 (MUST) 互不接管。

#### Scenario: 覆盖与回落

- **WHEN** 别名 X 写入 images 运行态覆盖(兜底 0.35/张)后删除该覆盖
- **THEN** 生效单价先为 0.35、删除后立即回落 YAML 兜底价(无 YAML 行则无价目,计量记 0)

#### Scenario: 档位命中与兜底

- **WHEN** 档位表含 `"1024x1024|standard": 0.30`,请求 quality=hd
- **THEN** 该请求按兜底单价计费(未命中档位不报错、不拒请求)

#### Scenario: 校验失败整单拒绝

- **WHEN** 写入请求含非法字段(币种非 CNY/USD、单价非十进制或 ≤0、档位键非 `size|quality` 形态、档位 JSON 非法)
- **THEN** 整单拒绝返回 400,不落库不刷新,目录保持原状

### Requirement: per-image 价目管理 API 契约

管理 API 必须 (MUST) 提供 `GET /admin/v1/image-pricing`(按别名升序)、`PUT /admin/v1/image-pricing/{alias}`(幂等覆盖)、`DELETE /admin/v1/image-pricing/{alias}`(撤销运行态覆盖,无覆盖返回 404),经管理面鉴权;在线改价必须 (MUST) 先校验后持久化再原子替换转发侧目录,下一请求生效零重启;脏数据必须 (MUST) 不进热路径(目录重建失败保持原状)。

#### Scenario: 改价下一请求生效

- **WHEN** 管理面成功写入别名 X 的新 images 价目
- **THEN** 后续 images 请求的计量行即按新价计费,服务全程无重启

#### Scenario: API 全流程

- **WHEN** 依次执行 GET → PUT → GET → DELETE → DELETE
- **THEN** 分别得到:yaml 来源条目(或空)→ 200 ok → db 来源条目与新价 → 200 ok → 404

### Requirement: chat 请求价目快照

chat 编排路径（含 messages/responses/gemini 入站）必须 (MUST) 在请求入口固定有效价目，预检、候选尝试、流中成本预算与最终落账共用该快照；在线改价与时段变化必须 (MUST) 只影响之后进入的请求。在入口无价目时，该请求必须 (MUST) 保持无价目、成本为零。embeddings 仍按完成时刻选价，images 保持独立入口价目快照。

#### Scenario: 在途改价不重定价

- **WHEN** chat 请求按旧价通过预算预检后，管理员在线更新该别名价格
- **THEN** 在途请求的预算和落账均使用入口旧价，下一请求使用新价

#### Scenario: 跨时段不重定价

- **WHEN** chat 请求在折扣时段开始，完成时已进入另一个计价时段
- **THEN** 全请求按入口命中的有效价结算，计量行保留该价格

#### Scenario: 在途新增价目

- **WHEN** chat 请求入口没有该别名价目，在生成期间管理员新增价目
- **THEN** 该请求成本仍为零，下一请求按新增价目结算

### Requirement: token 计费整数边界

token 计费必须 (MUST) 使用宽整数中间乘积并逐段向上取整后安全求和，保留缓存价缺省及钳制语义；可由 int64 表示的最终 credit 金额必须 (MUST) 精确计算，预算与落账共用该算法，不得因中间乘积超界而拒绝合法金额。最终费用不可表示时必须 (MUST) 保留原始 usage 并标记未结算观测（status=error、error_class=cost_overflow、estimated=true、cost_credits=0），输出请求身份、usage 与价目核对日志，不改变已生成的 HTTP 响应。

#### Scenario: 中间乘积超界但费用可表示

- **WHEN** 请求使用 100000000 个输入 token，输入价为 1000000000000 credit/百万 token
- **THEN** 实际成本精确为 100000000000000 credit，不回绕为负数或较小正数

#### Scenario: 最终金额超界

- **WHEN** 三段真实费用之和超过 int64 表示范围
- **THEN** 计量行保留原始 usage，标记 cost_overflow 未结算并输出可核对的价格与 usage 日志，不冒充正常零成本账单、不截顶

### Requirement: 视觉输入价目字段与回落

价目管理必须 (MUST) 增加可选 `vision_input` token 单价；缺省时必须 (MUST) 回落到 `input`。该字段必须 (MUST) 在 YAML、运行态 DB、管理 GET/PUT、OpenAPI 与控制台中保持同一含义，并沿用现有费率校验、币种、时段窗口、覆盖和 credit 整数运算纪律。

#### Scenario: 视觉单价缺省回落

- **WHEN** 别名只配置 `input` 而未配置 `vision_input`
- **THEN** 视觉 token 使用 `input` 单价，既有别名的实际费用保持不变

### Requirement: 多模态 token 分段整数计价

含 `vision_tokens` 的用量必须 (MUST) 将输入拆为文本、缓存和视觉三段并分别向上取整：`text_tokens=prompt_tokens-cached_tokens-vision_tokens`；`cost=ceil(text×input/1e6)+ceil(cached×cached_input/1e6)+ceil(vision×vision_input/1e6)+ceil(completion×output/1e6)`。当视觉 token 为 0 时必须 (MUST) 与现有计价结果一致。若 `cached_tokens+vision_tokens>prompt_tokens`，必须 (MUST) 按既有未结算观测纪律拒绝精确计价。

#### Scenario: 文本与视觉按不同单价结算

- **WHEN** `prompt_tokens=1000`、`vision_tokens=400`、`cached_tokens=0`，文本价为 1 credit/百万、视觉价为 2 credit/百万
- **THEN** 成本分别按 600 个文本 token 与 400 个视觉 token 向上取整，合计为 2 credits

#### Scenario: 无视觉用量零回归

- **WHEN** 既有请求 `vision_tokens=0` 并带有输入、缓存输入和输出 usage
- **THEN** 成本与原三段公式逐 credit 相同

#### Scenario: 非法 token 子集不作精确结算

- **WHEN** usage 中 `vision_tokens + cached_tokens > prompt_tokens`
- **THEN** 不产生精确费用，记录现有未结算/estimated 观测语义并输出可核对日志
